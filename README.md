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
| `GEMINI_ENABLED` | off | `1` with `GEMINI_API_KEY`: Gemini as an emergency fallback (text, search, transcription, tour TTS) |
