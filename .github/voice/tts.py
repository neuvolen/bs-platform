#!/usr/bin/env python3
"""BS voicepipe worker (GitHub Actions).

tts.py <jobs_dir> <out_dir>

Job file jobs/<name>.json:
  {"voice_id": "..."} or {"voice_query": "Stanislav"},
  "model": "eleven_multilingual_v2",
  "settings": {"stability": 0.45, "similarity": 0.8, "style": 0.2, "speed": 1.04, "speaker_boost": true},
  "context": true,            # pass previous/next line text for smoother prosody
  "lines": [{"id": "l01", "text": "..."}]

Output (branch voice-assets): out/<name>/<id>.mp3, out/<name>/<id>.json
(characters + words with start/end seconds), out/<name>/_job.json (status),
out/_account.json (voice ids, plan tier, character quota), out/_last_run.json.

ElevenLabs is reached through the platform server with a GitHub OIDC token;
no API key exists in this repository or in the runner.
"""
import base64, glob, hashlib, json, os, re, sys, time, urllib.error, urllib.parse, urllib.request

URL = os.environ.get("VOICEPIPE_URL", "https://app.bxclub.kz/api/v1/platform/voicepipe/eleven")
AUD = "bs-voicepipe"
TARGET_PLATFORM_VOICE = "ogi2DyUAKJb7CEdqqvlU"  # the platform tour's chosen library voice
DEFAULT_QUERY = "Stanislav"
DEFAULT_NAME = "Stanislav BS"

_tok = {"v": None, "at": 0}


def oidc():
    if _tok["v"] and time.time() - _tok["at"] < 240:
        return _tok["v"]
    u = os.environ["ACTIONS_ID_TOKEN_REQUEST_URL"] + "&audience=" + AUD
    req = urllib.request.Request(u, headers={"Authorization": "bearer " + os.environ["ACTIONS_ID_TOKEN_REQUEST_TOKEN"]})
    with urllib.request.urlopen(req, timeout=30) as r:
        _tok["v"] = json.load(r)["value"]
    _tok["at"] = time.time()
    return _tok["v"]


def el(method, path, query=None, body=None, tries=5):
    """One ElevenLabs call through the server: (status, parsed JSON or text)."""
    payload = json.dumps({"method": method, "path": path, "query": query or {}, "body": body}).encode()
    for t in range(tries):
        req = urllib.request.Request(URL, data=payload, method="POST", headers={
            "Authorization": "Bearer " + oidc(), "Content-Type": "application/json", "User-Agent": "bs-voicepipe"})
        try:
            with urllib.request.urlopen(req, timeout=200) as r:
                st, raw = r.status, r.read()
        except urllib.error.HTTPError as e:
            st, raw = e.code, e.read()
        except Exception as e:  # network
            st, raw = 0, str(e).encode()
        if st in (0, 429, 500, 502, 503, 504) and t < tries - 1:
            time.sleep(min(60, 2 ** (t + 1)))
            continue
        try:
            return st, json.loads(raw)
        except Exception:
            return st, raw.decode("utf-8", "replace")[:500]


def short(x):
    s = json.dumps(x, ensure_ascii=False) if not isinstance(x, str) else x
    return s[:400]


def account(out):
    acc = {"at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    st, sub = el("GET", "/v1/user/subscription")
    if st == 200:
        acc["tier"] = sub.get("tier")
        acc["status"] = sub.get("status")
        acc["character_count"] = sub.get("character_count")
        acc["character_limit"] = sub.get("character_limit")
        if isinstance(acc["character_count"], int) and isinstance(acc["character_limit"], int):
            acc["characters_left"] = acc["character_limit"] - acc["character_count"]
        acc["next_character_count_reset_unix"] = sub.get("next_character_count_reset_unix")
        acc["can_use_professional_voice_cloning"] = sub.get("can_use_professional_voice_cloning")
    else:
        acc["subscription_error"] = [st, short(sub)]
    st, models = el("GET", "/v1/models")
    if st == 200 and isinstance(models, list):
        acc["models"] = [m.get("model_id") for m in models if m.get("can_do_text_to_speech")]
    return acc


def find_voice(query, add_name, acc):
    """An account voice whose name contains query, else the library's, added to the account."""
    q = query.lower()
    st, mine = el("GET", "/v1/voices")
    if st == 200:
        for v in mine.get("voices", []):
            if q in (v.get("name") or "").lower():
                return v["voice_id"], v.get("name"), "account"
    else:
        acc.setdefault("warnings", []).append("GET /v1/voices %s %s" % (st, short(mine)))
    st, lib = el("GET", "/v1/shared-voices", {"search": query, "page_size": "30"})
    if st != 200:
        raise SystemExit("voice library search failed: %s %s" % (st, short(lib)))
    cands = [v for v in lib.get("voices", []) if (v.get("name") or "").lower().startswith(q)]
    if not cands:
        raise SystemExit("voice %r not found in the account or the library" % query)
    # «Stanislav - Deep, Empathetic and Warm» first, then by usage
    cands.sort(key=lambda v: (("deep" not in (v.get("name") or "").lower()) + ("warm" not in (v.get("name") or "").lower()),
                              -(v.get("cloned_by_count") or 0)))
    v = cands[0]
    acc.setdefault("library_candidates", [{"name": c.get("name"), "voice_id": c.get("voice_id"), "language": c.get("language")} for c in cands[:5]])
    st, added = el("POST", "/v1/voices/add/%s/%s" % (v["public_owner_id"], v["voice_id"]), body={"new_name": add_name})
    if st != 200:
        raise SystemExit("adding library voice %s failed: %s %s" % (v.get("name"), st, short(added)))
    return added.get("voice_id") or v["voice_id"], v.get("name"), "library→account"


def words_from(al):
    chars, starts, ends = al.get("characters", []), al.get("character_start_times_seconds", []), al.get("character_end_times_seconds", [])
    words, cur = [], None
    for i, ch in enumerate(chars):
        if ch.isspace():
            if cur:
                words.append(cur)
                cur = None
            continue
        if cur is None:
            cur = {"word": "", "start": starts[i], "end": ends[i], "char_start": i}
        cur["word"] += ch
        cur["end"] = ends[i]
    if cur:
        words.append(cur)
    for w in words:
        w["start"], w["end"] = round(w["start"], 3), round(w["end"], 3)
        w["clean"] = re.sub(r"^[^\w]+|[^\w]+$", "", w["word"])
    return words


def settings_body(s, model):
    s = s or {}
    vs = {"stability": s.get("stability", 0.45), "similarity_boost": s.get("similarity", s.get("similarity_boost", 0.8)),
          "style": s.get("style", 0.2), "use_speaker_boost": s.get("speaker_boost", True)}
    if "speed" in s:
        vs["speed"] = s["speed"]
    return vs


def render(job_path, out_root, acc, voices_cache):
    name = os.path.splitext(os.path.basename(job_path))[0]
    raw = open(job_path, "rb").read()
    job = json.loads(raw)
    sig = hashlib.sha256(raw).hexdigest()
    od = os.path.join(out_root, name)
    stp = os.path.join(od, "_job.json")
    if os.environ.get("FORCE") != "true" and os.path.exists(stp):
        try:
            if json.load(open(stp)).get("job_sha256") == sig and json.load(open(stp)).get("ok"):
                print("skip %s (unchanged)" % name)
                return True
        except Exception:
            pass
    os.makedirs(od, exist_ok=True)
    for f in glob.glob(os.path.join(od, "*")):
        os.remove(f)
    status = {"job": name, "job_sha256": sig, "run_id": os.environ.get("RUN_ID"), "jobs_sha": os.environ.get("JOBS_SHA"),
              "ok": False, "lines": [], "chars": 0}
    try:
        vid = job.get("voice_id")
        if not vid:
            q = job.get("voice_query") or DEFAULT_QUERY
            if q not in voices_cache:
                voices_cache[q] = find_voice(q, job.get("voice_add_name") or (DEFAULT_NAME if q == DEFAULT_QUERY else q + " BS"), acc)
            vid, vname, how = voices_cache[q]
            status["voice_name"], status["voice_resolved"] = vname, how
        status["voice_id"] = vid
        model = job.get("model") or "eleven_multilingual_v2"
        status["model"] = model
        vs = settings_body(job.get("settings"), model)
        status["voice_settings"] = vs
        lines = job.get("lines") or []
        use_ctx = job.get("context", True)
        for i, ln in enumerate(lines):
            lid, text = str(ln["id"]), ln["text"]
            if not re.fullmatch(r"[A-Za-z0-9_.-]{1,60}", lid):
                raise SystemExit("bad line id %r" % lid)
            body = {"text": text, "model_id": model, "voice_settings": vs}
            if ln.get("seed") is not None or job.get("seed") is not None:
                body["seed"] = ln.get("seed", job.get("seed"))
            if use_ctx:
                if i > 0:
                    body["previous_text"] = lines[i - 1]["text"]
                if i + 1 < len(lines):
                    body["next_text"] = lines[i + 1]["text"]
            st, r = el("POST", "/v1/text-to-speech/%s/with-timestamps" % vid, {"output_format": job.get("output_format", "mp3_44100_128")}, body)
            if st == 400 and use_ctx:  # a model without previous/next text
                body.pop("previous_text", None); body.pop("next_text", None)
                st, r = el("POST", "/v1/text-to-speech/%s/with-timestamps" % vid, {"output_format": job.get("output_format", "mp3_44100_128")}, body)
            if st != 200 or not isinstance(r, dict) or not r.get("audio_base64"):
                raise SystemExit("line %s: ElevenLabs %s %s" % (lid, st, short(r)))
            open(os.path.join(od, lid + ".mp3"), "wb").write(base64.b64decode(r["audio_base64"]))
            al = r.get("alignment") or {}
            words = words_from(al)
            meta = {"id": lid, "text": text, "voice_id": vid, "model": model, "voice_settings": vs,
                    "duration": (al.get("character_end_times_seconds") or [0])[-1],
                    "words": words, "alignment": al, "normalized_alignment": r.get("normalized_alignment")}
            json.dump(meta, open(os.path.join(od, lid + ".json"), "w"), ensure_ascii=False, indent=1)
            status["lines"].append({"id": lid, "chars": len(text), "words": len(words), "duration": meta["duration"]})
            status["chars"] += len(text)
            print("%s/%s ok: %d chars, %.2fs" % (name, lid, len(text), meta["duration"]))
            time.sleep(0.3)
        status["ok"] = True
    except SystemExit as e:
        status["error"] = str(e)
        print("ERROR %s: %s" % (name, e))
    json.dump(status, open(stp, "w"), ensure_ascii=False, indent=1)
    return status["ok"]


def platform_target(acc):
    """Can the platform tour's chosen library voice be read on this plan? (4 chars)"""
    t = {"voice_id": TARGET_PLATFORM_VOICE}
    st, v = el("GET", "/v1/voices/" + TARGET_PLATFORM_VOICE)
    t["in_account"] = st == 200
    if st == 200:
        t["name"] = v.get("name")
        t["category"] = v.get("category")
    st, r = el("POST", "/v1/text-to-speech/%s/with-timestamps" % TARGET_PLATFORM_VOICE, {"output_format": "mp3_22050_32"},
               {"text": "Тест.", "model_id": "eleven_multilingual_v2"})
    t["probe_status"] = st
    t["readable"] = st == 200
    if st != 200:
        t["probe_error"] = short(r)
    acc["platform_target"] = t


def main():
    jobs_dir, out_root = sys.argv[1], sys.argv[2]
    os.makedirs(out_root, exist_ok=True)
    acc = account(out_root)
    ok = True
    try:
        if acc.get("tier") in (None, "free"):
            raise SystemExit("ElevenLabs plan is %r: a paid plan is required (library voices, commercial license)" % acc.get("tier"))
        platform_target(acc)
        cache = {}
        q = DEFAULT_QUERY
        cache[q] = find_voice(q, DEFAULT_NAME, acc)
        acc["stanislav"] = {"voice_id": cache[q][0], "name": cache[q][1], "how": cache[q][2]}
        for jp in sorted(glob.glob(os.path.join(jobs_dir, "*.json"))):
            ok = render(jp, out_root, acc, cache) and ok
    except SystemExit as e:
        acc["error"] = str(e)
        print("ERROR: %s" % e)
        ok = False
    st, sub = el("GET", "/v1/user/subscription")
    if st == 200:
        acc["character_count_after"] = sub.get("character_count")
        if isinstance(sub.get("character_count"), int) and isinstance(sub.get("character_limit"), int):
            acc["characters_left_after"] = sub["character_limit"] - sub["character_count"]
    json.dump(acc, open(os.path.join(out_root, "_account.json"), "w"), ensure_ascii=False, indent=1)
    json.dump({"run_id": os.environ.get("RUN_ID"), "jobs_sha": os.environ.get("JOBS_SHA"), "ok": ok,
               "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())},
              open(os.path.join(out_root, "_last_run.json"), "w"), indent=1)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
