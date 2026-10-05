#!/usr/bin/env bash
# Re-record the tour voice (web/voice/*.mp3) after a phrase in bsTourTexts changed.
#   tools/voice/build.sh            new and changed phrases only, stale files removed
#   tools/voice/build.sh --force    everything again (after changing the voice or the processing)
#   CHECK=0 tools/voice/build.sh    skip the speech-recogniser read-back
#   tools/voice/build.sh --login    the login page demo instead (bsLoginTour in web/login.html → web/voice/login),
#                                   read ~15% faster (length_scale 1.1); add --force after changing the pace
# Voice: Piper ru_RU-dmitri-medium (CC0 dataset). The EQ in build.py is fitted to it;
# another voice (VOICE=denis) needs EQ refitted. ruslan is CC BY-NC-SA: not for this product.
# The GigaAM recogniser is a non-commercial model used only to check the files, never shipped.
# Needs python3, ffmpeg, network on the first run (pip, GitHub releases).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
VOICE="${VOICE:-dmitri}"
CACHE="${VOICE_CACHE:-$HOME/.cache/bs-voice}"
REL=https://github.com/k2-fsa/sherpa-onnx/releases/download
mkdir -p "$CACHE"
python3 -c "import piper, parselmouth, pyloudnorm, scipy, soundfile" 2>/dev/null || \
  pip install -q piper-tts praat-parselmouth pyloudnorm scipy soundfile numpy
M="$CACHE/vits-piper-ru_RU-$VOICE-medium"
if [ ! -f "$M/ru_RU-$VOICE-medium.onnx" ]; then
  curl -fsSL "$REL/tts-models/vits-piper-ru_RU-$VOICE-medium.tar.bz2" | tar xj -C "$CACHE"
fi
ASR=()
if [ "${CHECK:-1}" = 1 ]; then
  python3 -c "import sherpa_onnx" 2>/dev/null || pip install -q sherpa-onnx numpy
  A="$CACHE/sherpa-onnx-nemo-ctc-giga-am-russian-2024-10-24"
  [ -f "$A/model.int8.onnx" ] || curl -fsSL "$REL/asr-models/$(basename "$A").tar.bz2" | tar xj -C "$CACHE"
  ASR=(--asr "$A")
fi
python3 "$HERE/build.py" --model "$M/ru_RU-$VOICE-medium.onnx" --out "$REPO/web/voice" "${ASR[@]}" "$@"
