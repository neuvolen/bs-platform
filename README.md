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
| `ASR_DIR` | `<data dir>/bs-asr` (see «Persistent files») | the Whisper model, sherpa-onnx and ffmpeg (about 0.6 GB), downloaded once onto the volume |
| `ASR_PRELOAD` | off | `1`: download at start |
| `OPENAI_API_KEY` | none | Whisper API, fallback for transcription |
| `GEMINI_API_KEY` | none | free Google AI Studio key: text and web search (Google Search grounding) when Claude has no key or balance |
| `GEMINI_ENABLED` | auto | `0`: never use Gemini; `1`: also Gemini transcription fallback and tour TTS |
| `GROQ_API_KEY` | none | free Groq key (OpenAI-compatible), after Gemini |
| `OPENROUTER_API_KEY` | none | free OpenRouter `:free` models, after Groq |
| `AI_BUDGET_GEMINI` / `_GEMINI_SEARCH` / `_GROQ` / `_OPENROUTER` / `_CLAUDE` | 1000 / 100 / 900 / 45 / 0 | daily request budget per provider (0 = unlimited) to stay inside free limits |
| `AI_GEMINI_MODEL` / `AI_GEMINI_MODEL_LIGHT` | `gemini-3.8-flash` / `gemini-3.5-flash-lite` | Gemini models (heavy and default / short JSON tasks). R55: Google counts quota per model, so while the main model's quota is closed (e.g. a free key whose tier gives it `limit: 0`) the light model answers instead; the 429 log line names the metric, limit and model |
| `AI_GROQ_MODEL` / `AI_GROQ_MODEL_LIGHT` | `openai/gpt-oss-120b` / `openai/gpt-oss-20b` | Groq models |
| `AI_OPENROUTER_MODEL` | `openrouter/free` | OpenRouter model (the free router) |
| `AI_TEXT_ORDER` | `claude,gemini,groq,openrouter,openai` | the order of the text chain |

## Persistent files (R55)

The Railway service `bs-platform` has the volume **bs-data** (5 GB) mounted at `/data`. `internal/datadir` picks the folder: `BS_DATA_DIR` if set, else `/data` when it exists and is writable, else the container's temp dir (lost on the next deploy). The start log says which: `data: video folder … (persistent true, writable true, cap 2500 MB, …)`.

| Folder | What | Cap |
|---|---|---|
| `bs-video/` | Reels uploads and renders | `VIDEO_MAX_MB` (2500): past it the oldest finished Reels go first, then the oldest uploads no job waits for; plus 24 h / 12 Reels / 14 days |
| `bs-asr/` | Whisper model, sherpa-onnx, ffmpeg | about 0.6 GB, fixed |

Files that must never be lost (finished Reels up to 80 MB, the funnel videos, PDFs) are kept in Postgres `platform_files` as well.

## Funnel videos (R55): Маркетинг → SMM → «Видео в воронке»

The bot sends a lead one video per funnel step, once: `start` (20 min after /start, the checklists given), `d1` `d3` `d7` `d10` `d14` (before that day's warm-up touch), `booked` (after booking the express-разбор), `offer` (after the разбор, with the club offer). Start and warm-up videos carry the «Записаться на экспресс-разбор» button. The file is uploaded to Telegram once; its `file_id` is kept in the library (server doc `bs_funnel_videos`), the bytes in `platform_files`.

Add a video either in the platform (upload MP4 H.264 up to 20 MB, pick the step) or in the repo: drop `<name>.mp4` (up to 8 MB, 720×1280: `ffmpeg -i in.mp4 -vf scale=720:1280 -c:v libx264 -b:v 1500k -maxrate 1800k -bufsize 3000k -c:a aac -b:a 96k -movflags +faststart out.mp4`) into `web/funnel_video/` and describe it in `videos.json` (`file, title, topic, step, caption, w, h, dur`). It is copied in once at start; the platform's later changes win and a video deleted there is not brought back. The step's numbers: `GET /api/v1/platform/sales/funnel/stats?days=14` and the daily log line `funnel stats 14d: …`.

## Kaspi (R55)

`bot.KaspiLink` (`https://pay.kaspi.kz/pay/ri6h2lj5`) is the owner's single Kaspi link (fines, express-разбор, breakfast, club). It opens Kaspi without a sum, so every message with it names the sum to type. At start it goes once into an empty «Ссылка на оплату клуба» (Продажи → настройки) and into open paid events without a link; a link the team clears later stays cleared (`kaspiSeeded`, server doc `bs_paylink_seed`).

## Reels editor (R54): Маркетинг → SMM → «Видео»

The owner uploads clips (8 MB chunks, up to 500 MB per file), picks a template («Говорящая голова», «Б-ролл с подписями», «Чистая склейка») and the server cuts a Reels with ffmpeg in the background (`internal/video`, `internal/handlers/http/platform_video.go`): 1080×1920 centre crop (or the whole frame on a blurred copy), pauses cut (silencedetect -35 dB / 0.5 s, 80 ms padding), word-by-word captions from the local Whisper (phrase times from sherpa-onnx, words spread by length; Manrope ExtraBold, the spoken word inverted, 2-3 words per page), a hook card for the first 1.5 s, punch-ins on emphasis, the black outro with the logo and the CTA, music ducked under the voice (sidechain), -14 LUFS / -1 dBTP, H.264 High + AAC, `+faststart`, a cover PNG.

| Limit | Value |
|---|---|
| file / chunk | 500 MB / 8 MB (16 MB max) |
| clips in one Reels | 10, at most 180 s in total |
| jobs | one render at a time, up to 3 waiting, 30 min timeout (`VIDEO_TIMEOUT`, minutes) |
| storage | `VIDEO_DIR`, default `<data dir>/bs-video` (the volume bs-data at `/data`, see «Persistent files»), capped at `VIDEO_MAX_MB`; uploads kept 24 h, the last 12 Reels for 14 days; a Reels up to 80 MB and its cover are also kept in `platform_files`, so the signed link in the SMM plan survives a deploy |
| ffmpeg | the image's own if present, else the static build downloaded at start (same one as the call transcription); needs `ass`, `loudnorm`, `sidechaincompress`, `libx264` (checked at start: log line `video: ffmpeg …`). To put ffmpeg into the image instead: Railway variable `RAILPACK_DEPLOY_APT_PACKAGES=ffmpeg`. `VIDEO_FFMPEG` points to another binary, `VIDEO_PRELOAD=0` skips the start-up check |

Credit: the short-form numbers (Reels safe zones 250/350 px, 2-4 words per caption page, the silence-cut settings, -14 LUFS, music about 10 dB under the voice) follow the craft notes of [OpenMontage](https://github.com/calesthio/OpenMontage) by Calesthio (AGPL-3.0). No OpenMontage code is used or shipped: it is an agent toolkit for a local machine (Python, Remotion, paid generation APIs), so only these published facts were taken and the pipeline here is written from scratch.
