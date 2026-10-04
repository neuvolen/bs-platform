#!/usr/bin/env python3
"""Pre-recorded voice of the onboarding tour (R32c).

Every spoken phrase of the tour (the bsTourTexts block of web/platform.html,
the same list web/tour.go reads) becomes a small static file
web/voice/<sha256(text)[:16]>.mp3, embedded into the binary and served with a
one-year cache. The page plays these files and never waits for an API.

Pipeline per phrase:
  1. pronunciation: Latin words, numbers and abbreviations spelled for Russian TTS (SAY)
  2. Piper neural TTS, Russian male voice (ru_RU-dmitri-medium by default)
  3. «digital assistant» colour with ffmpeg: slight pitch down, low-mix ring
     modulation, short metallic comb echo, light chorus, presence EQ
  4. two-pass EBU R128 loudness normalisation to -16 LUFS, true peak -1.5 dB
  5. a quiet two-tone blip before the phrase, mp3 mono 48 kbit/s
  6. checks: duration grows with text length, no clipping; with --asr also a
     Russian speech recogniser (sherpa-onnx GigaAM) reads every file back

Run tools/voice/build.sh after changing a tour phrase; `go test ./web` fails
while a phrase has no file.
"""
import argparse, hashlib, json, os, re, subprocess, sys, tempfile, wave

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# How the voice says what is written. Applied in order, whole words.
SAY = [
    (r"Business Surgery", "Бизнес Сёрджери"),
    (r"\bBS\b", "Би Эс"),
    (r"\bPL\b", "пи энд эл"),
    (r"\bCRM\b", "эс эр эм"),
    (r"\bCtrl K\b", "Контрол Кей"),
    (r"\bGallup\b", "Геллап"),
    (r"\bИИ\b", "искусственного интеллекта"),
    (r"до 22:00", "до десяти вечера"),
    (r"10 000 тенге", "десять тысяч тенге"),
    (r"«|»", ""),
    # «Клуб. Ресурсы…»: a one-word sentence is swallowed, a colon keeps it in one breath
    (r"^([^\s.:]+(?: [^\s.:]+)?)\. ", r"\1: "),
]

FX = ("aresample=48000,highpass=f=70,rubberband=pitch=0.944:formant=shifted,"
      "asplit=2[dry][rm];"
      "[rm]aeval='val(0)*sin(2*PI*180*t)':c=same,volume=0.11[rmo];"
      "[dry][rmo]amix=inputs=2:weights='1 1':normalize=0,"
      "aecho=0.9:0.9:7|13:0.16|0.10,"
      "chorus=0.9:0.9:22|31:0.12|0.09:0.25|0.4:1.2|1.6,"
      "equalizer=f=220:t=o:w=1.2:g=-2.5,equalizer=f=3200:t=o:w=1.0:g=3.5,"
      "equalizer=f=7500:t=o:w=1.2:g=1.5,lowpass=f=11500")


def tour_texts(html):
    m = re.search(r'<script type="application/json" id="bsTourTexts">(.*?)</script>', html, re.S)
    if not m:
        sys.exit("bsTourTexts block not found")
    d = json.loads(m.group(1))
    out = []

    def add(s):
        s = " ".join((s or "").split())
        if s and s not in out:
            out.append(s)
    for v in (d.get("welcome") or {}).values():
        add(v.get("d"))
    for v in (d.get("cta") or {}).values():
        add(v)
    for v in (d.get("nav") or {}).values():
        add(v)
    for a in d.get("actions") or []:
        add(a.get("d"))
    add((d.get("help") or {}).get("d"))
    add((d.get("bye") or {}).get("d"))
    return out


def key(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:16]


def spoken(text):
    s = text
    for a, b in SAY:
        s = re.sub(a, b, s)
    return s


def sh(args, **kw):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kw)


def loudnorm_two_pass(src, dst):
    p = subprocess.run(["ffmpeg", "-hide_banner", "-nostats", "-i", src, "-af",
                        "loudnorm=I=-16:TP=-2:LRA=7:print_format=json", "-f", "null", "-"],
                       capture_output=True, text=True)
    j = json.loads(p.stderr[p.stderr.rindex("{"):p.stderr.rindex("}") + 1])
    af = ("loudnorm=I=-16:TP=-2:LRA=7:linear=true:measured_I={input_i}:measured_TP={input_tp}:"
          "measured_LRA={input_lra}:measured_thresh={input_thresh}:offset={target_offset},"
          "aresample=48000").format(**j)
    sh(["ffmpeg", "-y", "-hide_banner", "-i", src, "-af", af, "-ar", "48000", dst])


def blip(path):
    # two soft sine pips (1320 Hz, 1760 Hz), 45 ms each, then 150 ms of silence
    expr = ("0.11*(if(lt(t,0.045),sin(2*PI*1320*t)*sin(PI*t/0.045),0)"
            "+if(between(t,0.06,0.105),sin(2*PI*1760*t)*sin(PI*(t-0.06)/0.045),0))")
    sh(["ffmpeg", "-y", "-hide_banner", "-f", "lavfi", "-i",
        f"aevalsrc='{expr}':s=48000:d=0.255", "-ac", "1", path])


def probe(path):
    p = subprocess.run(["ffmpeg", "-hide_banner", "-nostats", "-i", path, "-af",
                        "ebur128=peak=true", "-f", "null", "-"], capture_output=True, text=True)
    i = re.findall(r"I:\s+(-?[\d.]+) LUFS", p.stderr)
    pk = re.findall(r"Peak:\s+(-?[\d.]+|-inf) dBFS", p.stderr)
    d = float(sh(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path]).stdout)
    return d, float(i[-1]) if i else None, float(pk[-1]) if pk and pk[-1] != "-inf" else None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--html", default=os.path.join(ROOT, "web/platform.html"))
    ap.add_argument("--out", default=os.path.join(ROOT, "web/voice"))
    ap.add_argument("--model", required=True, help="Piper .onnx (its .onnx.json next to it)")
    ap.add_argument("--raw", action="store_true", help="no effect (for voice comparison)")
    ap.add_argument("--asr", help="sherpa-onnx GigaAM CTC model dir: read every file back")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--length-scale", type=float, default=0.97)
    a = ap.parse_args()

    from piper import PiperVoice, SynthesisConfig
    voice = PiperVoice.load(a.model)
    cfg = SynthesisConfig(length_scale=a.length_scale, noise_scale=0.6, noise_w_scale=0.75)
    texts = tour_texts(open(a.html, encoding="utf-8").read())
    os.makedirs(a.out, exist_ok=True)
    tmp = tempfile.mkdtemp()
    bp = os.path.join(tmp, "blip.wav")
    blip(bp)
    index, report = {}, []
    for t in texts:
        k = key(t)
        dst = os.path.join(a.out, k + ".mp3")
        index[t] = k + ".mp3"
        if os.path.exists(dst) and not a.force:
            continue
        w0, w1, w2, w3 = (os.path.join(tmp, k + s) for s in ("_0.wav", "_1.wav", "_2.wav", "_3.wav"))
        with wave.open(w0, "wb") as wf:
            voice.synthesize_wav(spoken(t), wf, syn_config=cfg)
        # 60 ms of air on both ends so the chorus and echo tails are not cut
        pad = "adelay=60:all=1,apad=pad_dur=0.25"
        sh(["ffmpeg", "-y", "-hide_banner", "-i", w0, "-filter_complex",
            "aresample=48000,highpass=f=70,lowpass=f=11500," + pad if a.raw else FX + "," + pad, "-ac", "1", w1])
        loudnorm_two_pass(w1, w2)
        if a.raw:
            w3 = w2
        else:
            sh(["ffmpeg", "-y", "-hide_banner", "-i", bp, "-i", w2, "-filter_complex",
                "[0][1]concat=n=2:v=0:a=1", "-ac", "1", w3])
        sh(["ffmpeg", "-y", "-hide_banner", "-i", w3, "-c:a", "libmp3lame", "-b:a", "48k",
            "-ar", "24000", "-ac", "1", "-id3v2_version", "0", "-write_xing", "1", dst])
    # files of phrases that are gone
    keep = set(index.values())
    for f in os.listdir(a.out):
        if f.endswith(".mp3") and f not in keep:
            os.remove(os.path.join(a.out, f))
    with open(os.path.join(a.out, "index.json"), "w", encoding="utf-8") as f:
        json.dump(index, f, ensure_ascii=False, indent=1, sort_keys=True)
        f.write("\n")

    rec = None
    if a.asr:
        import sherpa_onnx
        rec = sherpa_onnx.OfflineRecognizer.from_nemo_ctc(
            model=os.path.join(a.asr, "model.int8.onnx"), tokens=os.path.join(a.asr, "tokens.txt"), num_threads=4)
    bad = 0
    total_cer = []
    for t in texts:
        p = os.path.join(a.out, index[t])
        d, lufs, peak = probe(p)
        rate = len(t) / max(d - 0.6, 0.1)
        line = {"file": index[t], "chars": len(t), "dur": round(d, 2), "cps": round(rate, 1),
                "lufs": lufs, "peak": peak, "kb": os.path.getsize(p) // 1024}
        if not (9 <= rate <= 22) or (peak is not None and peak > -0.5):
            line["warn"] = True
            bad += 1
        if rec:
            import numpy as np
            pcm = subprocess.run(["ffmpeg", "-v", "error", "-i", p, "-f", "s16le", "-ac", "1", "-ar", "16000", "-"],
                                 capture_output=True).stdout
            s = rec.create_stream()
            s.accept_waveform(16000, np.frombuffer(pcm, dtype=np.int16).astype(np.float32) / 32768)
            rec.decode_stream(s)
            hyp = s.result.text.strip().lower()
            ref = re.sub(r"[^а-яё ]", " ", spoken(t).lower().replace("+", ""))
            ref = " ".join(ref.split())
            line["asr"] = hyp
            line["cer"] = round(cer(ref.replace("ё", "е"), hyp.replace("ё", "е")), 3)
            total_cer.append(line["cer"])
        report.append(line)
    with open(os.path.join(tmp, "report.json"), "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=1)
    for r in report:
        print(json.dumps(r, ensure_ascii=False))
    if total_cer:
        print("mean CER %.3f" % (sum(total_cer) / len(total_cer)))
    print("%d phrases, %d warnings, report %s" % (len(texts), bad, os.path.join(tmp, "report.json")))


def cer(ref, hyp):
    ref, hyp = ref.replace(" ", ""), hyp.replace(" ", "")
    prev = list(range(len(hyp) + 1))
    for i, rc in enumerate(ref, 1):
        cur = [i]
        for j, hc in enumerate(hyp, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (rc != hc)))
        prev = cur
    return prev[-1] / max(1, len(ref))


if __name__ == "__main__":
    main()
