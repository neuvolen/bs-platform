# business_surgery_backend

Backend API service for managing and automating business operations and scheduling.

## AI (R34a)

Claude does all text, JSON, reasoning and web search (`internal/ai/claude.go`).
Recordings of online разборы are transcribed on the server by Whisper via
sherpa-onnx (`internal/ai/asr_local.go`); nothing is needed at build time, the
tools and the model are downloaded on the first recording.

| Env | Default | What |
|---|---|---|
| `ANTHROPIC_API_KEY` | none | Claude key. Without it the key saved in platform Settings («Ключ Claude», admin only, AES-GCM sealed) is used |
| `AI_KEYS_SECRET` | `JWT_SECRET` | seals the key saved in Settings |
| `AI_MODEL` | `claude-sonnet-5` | most tasks |
| `AI_MODEL_HEAVY` | `claude-opus-5` | Gallup deep analysis, разбор summary (falls back to `AI_MODEL`) |
| `AI_WEB_SEARCH_TOOL` | `web_search_20250305` | web search server tool version |
| `ASR_LOCAL` | on | `0` turns server transcription off |
| `ASR_MODEL` | `small` | `tiny`, `base`, `small`, `medium`, `turbo` |
| `ASR_DIR` | `/data/bs-asr` if `/data` is writable, else tmp | put on a Railway volume to keep the download across deploys |
| `ASR_PRELOAD` | off | `1`: download at start |
| `OPENAI_API_KEY` | none | Whisper API, fallback for transcription |
| `GEMINI_API_KEY` | none | free Google AI Studio key: text and web search (Google Search grounding) when Claude has no key or balance |
| `GEMINI_ENABLED` | auto | `0`: never use Gemini; `1`: also Gemini transcription fallback and tour TTS |
| `GROQ_API_KEY` | none | free Groq key (OpenAI-compatible), after Gemini |
| `OPENROUTER_API_KEY` | none | free OpenRouter `:free` models, after Groq |
| `AI_BUDGET_GEMINI` / `_GEMINI_SEARCH` / `_GROQ` / `_OPENROUTER` / `_CLAUDE` | 1000 / 100 / 900 / 45 / 0 | daily request budget per provider (0 = unlimited) to stay inside free limits |
| `AI_GEMINI_MODEL` / `AI_GEMINI_MODEL_LIGHT` | `gemini-3.8-flash` / `gemini-3.5-flash-lite` | Gemini models (heavy and default / short JSON tasks) |
| `AI_GROQ_MODEL` / `AI_GROQ_MODEL_LIGHT` | `openai/gpt-oss-120b` / `openai/gpt-oss-20b` | Groq models |
| `AI_OPENROUTER_MODEL` | `openrouter/free` | OpenRouter model (the free router) |
| `AI_TEXT_ORDER` | `claude,gemini,groq,openrouter,openai` | the order of the text chain |

## Reels editor (R54): Маркетинг → SMM → «Видео»

The owner uploads clips (8 MB chunks, up to 500 MB per file), picks a template («Говорящая голова», «Б-ролл с подписями», «Чистая склейка») and the server cuts a Reels with ffmpeg in the background (`internal/video`, `internal/handlers/http/platform_video.go`): 1080×1920 centre crop (or the whole frame on a blurred copy), pauses cut (silencedetect -35 dB / 0.5 s, 80 ms padding), word-by-word captions from the local Whisper (phrase times from sherpa-onnx, words spread by length; Manrope ExtraBold, the spoken word inverted, 2-3 words per page), a hook card for the first 1.5 s, punch-ins on emphasis, the black outro with the logo and the CTA, music ducked under the voice (sidechain), -14 LUFS / -1 dBTP, H.264 High + AAC, `+faststart`, a cover PNG.

| Limit | Value |
|---|---|
| file / chunk | 500 MB / 8 MB (16 MB max) |
| clips in one Reels | 10, at most 180 s in total |
| jobs | one render at a time, up to 3 waiting, 30 min timeout (`VIDEO_TIMEOUT`, minutes) |
| storage | `VIDEO_DIR`, default `/data/bs-video` when a Railway volume is mounted at `/data`, else the container's temp dir (gone after a deploy); uploads kept 24 h, the last 12 Reels for 14 days; a Reels up to 80 MB and its cover are also kept in `platform_files`, so the signed link in the SMM plan survives a deploy |
| ffmpeg | the image's own if present, else the static build downloaded at start (same one as the call transcription); needs `ass`, `loudnorm`, `sidechaincompress`, `libx264` (checked at start: log line `video: ffmpeg …`). To put ffmpeg into the image instead: Railway variable `RAILPACK_DEPLOY_APT_PACKAGES=ffmpeg`. `VIDEO_FFMPEG` points to another binary, `VIDEO_PRELOAD=0` skips the start-up check |

Credit: the short-form numbers (Reels safe zones 250/350 px, 2-4 words per caption page, the silence-cut settings, -14 LUFS, music about 10 dB under the voice) follow the craft notes of [OpenMontage](https://github.com/calesthio/OpenMontage) by Calesthio (AGPL-3.0). No OpenMontage code is used or shipped: it is an agent toolkit for a local machine (Python, Remotion, paid generation APIs), so only these published facts were taken and the pipeline here is written from scratch.
