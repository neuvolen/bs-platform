#!/usr/bin/env bash
# Re-record the tour voice (web/voice/*.mp3) after a phrase in bsTourTexts changed.
#   tools/voice/build.sh            new and changed phrases only, stale files removed
#   tools/voice/build.sh --force    everything again (after changing the voice or the effect)
#   VOICE=dmitri tools/voice/build.sh --force   another Piper voice
# Needs python3, ffmpeg (with rubberband), network on the first run (pip, GitHub releases).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
VOICE="${VOICE:-denis}"
CACHE="${VOICE_CACHE:-$HOME/.cache/bs-voice}"
REL=https://github.com/k2-fsa/sherpa-onnx/releases/download
mkdir -p "$CACHE"
python3 -c "import piper" 2>/dev/null || pip install -q piper-tts
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
