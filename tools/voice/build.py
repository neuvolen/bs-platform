#!/usr/bin/env python3
"""Pre-recorded voice of the onboarding tour (R32c, voice R33).

Every spoken phrase of the tour (the bsTourTexts block of web/platform.html,
the same list web/tour.go reads) becomes a small static file
web/voice/<sha256(text)[:16]>.mp3, embedded into the binary and served with a
one-year cache. The page plays these files and never waits for an API.

Style target: a calm, low, measured, polite male «AI butler» with light studio
processing. No speaker cloning: the base is a stock CC0 Piper voice, only the
delivery and the mix were matched to the owner's reference (objective analysis:
median F0 ~116 Hz, 5-95% pitch range ~7-11 semitones, ~5.1 syllables/s,
dark long-term spectrum with a step down above ~3-4 kHz, light short stereo
ambience at about -17 dB, no chorus, no ring modulation, no flanger).

Pipeline per phrase:
  1. pronunciation: Latin words, numbers and abbreviations spelled for Russian TTS (SAY),
     then espeak stress errors patched on the phoneme string (STRESS); every sentence
     is synthesised on its own and joined with a pause
  2. Piper neural TTS ru_RU-dmitri-medium (CC0 dataset), slower (length_scale 1.3),
     sentences joined with a 0.34 s breath
  3. Praat PSOLA (formants untouched): pitch median 136 Hz, pitch range x0.8, tempo x1/1.08
  4. EQ matched to the reference long-term spectrum (EQ, 1/3-octave, 1-octave smoothed),
     high-pass 60 Hz, low-pass 10.5 kHz
  5. gentle compression (2.5:1, RMS), a short dark plate at -19 dB (deterministic)
  6. loudness -16 LUFS (EBU R128, measured on the final mp3), limiter -1.9 dBFS,
     mp3 mono 24 kHz 48 kbit/s
  7. checks: duration grows with text length, no clipping; with --asr also a
     Russian speech recogniser (sherpa-onnx GigaAM, check only, never shipped)
     reads every file back

Run tools/voice/build.sh after changing a tour phrase; `go test ./web` fails
while a phrase has no file.
"""
import argparse, hashlib, json, os, re, subprocess, sys, tempfile

import numpy as np
import scipy.signal as ss
import soundfile as sf

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SR = 48000

# How the voice says what is written. Applied in order, whole words.
SAY = [
    (r"Business Surgery", "Бизнес Сёрджери"),
    (r"\bBS\b", "Би Эс"),
    (r"\bPL\b", "пи энд эл"),
    (r"\bCRM\b", "эс эр эм"),
    (r"\bCtrl K\b", "Контрол Кей"),
    (r"\bGallup\b", "Геллап"),
    (r"\bГэллап\b", "Геллап"),
    (r"\bИИ\b", "искусственного интеллекта"),
    (r"до 22:00", "до десяти вечера"),
    (r"10 000 тенге", "десять тысяч тенге"),
    (r"«|»", ""),
    # «Клуб. Ресурсы…»: a one-word sentence is swallowed, a colon keeps it in one breath
    (r"^([^\s.:]+(?: [^\s.:]+)?)\. ", r"\1: "),
]

# espeak-ng puts some stresses wrong; fixed on its phoneme output (wrong, right)
STRESS = [
    ("skrʲiptˈy", "skrʲˈipty"),      # скрИпты
    ("ɭʲistˈɑmʲɪ", "ɭʲˈistʌmʲɪ"),   # чек-лИстами
    ("ɭʲistˈy", "ɭʲˈisty"),          # чек-лИсты
    ("ɭʲistˈof", "ɭʲˈistʌf"),        # чек-лИстов
    ("zˈɑmʲir", "zʌmʲˈer"),          # замЕр
    ("sammˈɑrʲɪ", "sˈammʌrʲɪ"),      # сАммари
    ("ʃtotˈo", "ʃtˈotʌ"),            # чтО-то
    ("ɡʲiɭɭˈɑp", "ɡˈɛɭʌp"),          # ГЭллап
    ("trʲikʲˈink", "trʲˈekʲink"),    # трЕкинг
    ("bʲˈɪ ˈɛs", "bʲˈi ˈɛs"),        # Би Эс, a clear «и»
    ("pʲˈɪ ˈɛnt ˈɛɭ", "pʲˈi ˈɛnd ˈɛɭ"),
]

LENGTH_SCALE = 1.3
# R40d: the login demo reads about 15% faster than the tour (owner: «побыстрее»)
LOGIN_LENGTH_SCALE = 1.1
SENTENCE_GAP = 0.34
PITCH_MEDIAN, PITCH_RANGE, TEMPO_STRETCH = 136.0, 0.8, 1.08

# Matched EQ, dB per 1/3-octave band from 39 Hz to 10 kHz: reference long-term
# spectrum minus the spectrum of steps 2-3 over all tour phrases, smoothed over
# one octave, lows capped at +4 dB (fitted for dmitri with the settings above).
EQ_CENTERS = 1000 * 2 ** (np.arange(-14, 11) / 3)
EQ = [4.0, 4.0, 4.0, 4.0, 4.0, -0.0, 2.3, 3.4, 6.9, 3.8, 2.2, -1.0, -3.0, -3.1, -2.7, -1.9,
      -1.2, -0.6, -2.2, -4.0, -5.4, -4.8, -4.4, -2.6, -1.4]
COMP = "acompressor=threshold=0.1:ratio=2.5:attack=8:release=140:knee=3:detection=rms"
PLATE_DB = -19.0


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
    for st in d.get("rsteps") or []:  # R81: the resident's tour, section by section
        add(st.get("d"))
    add((d.get("help") or {}).get("d"))
    add((d.get("bye") or {}).get("d"))
    return out


def login_texts(html):
    """R40c: the guided demo of the login page (web/login.html, bsLoginTour): the spoken line v of every step."""
    m = re.search(r'<script type="application/json" id="bsLoginTour">(.*?)</script>', html, re.S)
    if not m:
        sys.exit("bsLoginTour block not found")
    out = []
    for st in json.loads(m.group(1)).get("steps") or []:
        s = " ".join((st.get("v") or "").split())
        if s and s not in out:
            out.append(s)
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


# ---------------------------------------------------------------- synthesis

def trim(a, sr, db=-45):
    fr = int(0.005 * sr)
    e = np.array([np.abs(a[i:i + fr]).max() for i in range(0, len(a), fr)]) + 1e-9
    on = np.where(20 * np.log10(e / e.max()) > db)[0]
    return a[max(0, on[0] - 2) * fr:(on[-1] + 3) * fr]


def synth(voice, text, length_scale):
    from piper import SynthesisConfig
    cfg = SynthesisConfig(length_scale=length_scale, noise_scale=0.5, noise_w_scale=0.7)
    sr = voice.config.sample_rate
    out = []
    for ph in voice.phonemize(text):
        s = "".join(ph)
        for a, b in STRESS:
            s = s.replace(a, b)
        a = np.asarray(voice.phoneme_ids_to_audio(voice.phonemes_to_ids(list(s)), cfg), dtype=np.float64).reshape(-1)
        if out:
            out.append(np.zeros(int(SENTENCE_GAP * sr)))
        out.append(trim(a, sr))
    x = np.concatenate(out)
    g = np.gcd(SR, sr)
    return ss.resample_poly(x, SR // g, sr // g)


def psola(x):
    """Praat overlap-add «Change gender» with formant shift 1.0: pitch and tempo only."""
    import parselmouth
    from parselmouth.praat import call
    y = call(parselmouth.Sound(x, SR), "Change gender", 60, 400, 1.0, PITCH_MEDIAN, PITCH_RANGE, TEMPO_STRETCH)
    return y.values[0]


# ---------------------------------------------------------------- processing

def eq_fir(ntaps=2047):
    fr = np.concatenate([[0], EQ_CENTERS, [SR / 2]])
    g = np.concatenate([[EQ[0] - 12], EQ, [EQ[-1] - 30]])
    return ss.firwin2(ntaps, fr, 10 ** (g / 20), fs=SR)


def plate(rt60=0.42, pre=0.012):
    rng = np.random.default_rng(7)
    n = int(rt60 * SR)
    ir = rng.standard_normal(n) * np.exp(-6.91 * np.arange(n) / SR / rt60)
    ir = ss.lfilter(*ss.butter(2, 6000 / (SR / 2)), ir)
    ir = ss.lfilter(*ss.butter(2, 300 / (SR / 2), "high"), ir)
    return np.concatenate([np.zeros(int(pre * SR)), ir / np.sqrt((ir ** 2).sum())])


def ffwav(x, af, tmp):
    a, b = os.path.join(tmp, "ff_in.wav"), os.path.join(tmp, "ff_out.wav")
    sf.write(a, x, SR, subtype="FLOAT")
    sh(["ffmpeg", "-y", "-hide_banner", "-i", a, "-af", af, "-c:a", "pcm_f32le", b])
    return sf.read(b)[0]


def process(x, tmp):
    x = ss.lfilter(*ss.butter(2, 60 / (SR / 2), "high"), x)
    h = eq_fir()
    x = ss.fftconvolve(x, h)[len(h) // 2:][:len(x)]
    x = ss.lfilter(*ss.butter(4, 10500 / (SR / 2)), x)
    x = ffwav(x / (np.abs(x).max() + 1e-9) * 0.5, COMP, tmp)
    x = np.concatenate([np.zeros(int(0.06 * SR)), x, np.zeros(int(0.35 * SR))])
    w = ss.fftconvolve(x, plate())[:len(x)]
    return x + w * np.sqrt((x ** 2).sum() / ((w ** 2).sum() + 1e-12)) * 10 ** (PLATE_DB / 20)


def encode(x, dst, tmp, target=-16.0):
    """Gain to the target loudness as measured on the final mp3, limiter at -1.9 dBFS."""
    import pyloudnorm
    w = os.path.join(tmp, "enc.wav")
    gain = target - pyloudnorm.Meter(SR).integrated_loudness(x)
    for _ in range(3):
        sf.write(w, x * 10 ** (gain / 20), SR, subtype="FLOAT")
        sh(["ffmpeg", "-y", "-hide_banner", "-i", w, "-af",
            "aresample=192000,alimiter=limit=0.8:attack=2:release=60:level=disabled,aresample=24000",
            "-c:a", "libmp3lame", "-b:a", "48k", "-ar", "24000", "-ac", "1",
            "-id3v2_version", "0", "-write_xing", "1", dst])
        lufs = probe(dst)[1]
        if lufs is None or abs(lufs - target) < 0.15:
            break
        gain += target - lufs


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
    ap.add_argument("--raw", action="store_true", help="plain TTS, no pitch/EQ/room (for voice comparison)")
    ap.add_argument("--asr", help="sherpa-onnx GigaAM CTC model dir: read every file back")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--login", action="store_true", help="the login page demo (web/login.html → web/voice/login)")
    ap.add_argument("--length-scale", type=float, default=None)
    a = ap.parse_args()
    if a.length_scale is None:
        a.length_scale = LOGIN_LENGTH_SCALE if a.login else LENGTH_SCALE

    from piper import PiperVoice
    voice = PiperVoice.load(a.model)
    if a.login:
        if a.html.endswith("platform.html"):
            a.html = os.path.join(ROOT, "web/login.html")
        if a.out.rstrip("/").endswith("web/voice"):
            a.out = os.path.join(a.out, "login")
        texts = login_texts(open(a.html, encoding="utf-8").read())
    else:
        texts = tour_texts(open(a.html, encoding="utf-8").read())
    os.makedirs(a.out, exist_ok=True)
    tmp = tempfile.mkdtemp()
    index, report = {}, []
    for t in texts:
        k = key(t)
        dst = os.path.join(a.out, k + ".mp3")
        index[t] = k + ".mp3"
        if os.path.exists(dst) and not a.force:
            continue
        x = synth(voice, spoken(t), a.length_scale)
        if a.raw:
            x = np.concatenate([np.zeros(int(0.06 * SR)), x, np.zeros(int(0.25 * SR))])
        else:
            x = process(psola(x), tmp)
        encode(x, dst, tmp)
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
        if not (8 <= rate <= 22) or (peak is not None and peak > -0.5):
            line["warn"] = True
            bad += 1
        if rec:
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
        print("mean CER %.3f (character accuracy %.1f%%)" % (sum(total_cer) / len(total_cer),
                                                               100 * (1 - sum(total_cer) / len(total_cer))))
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
