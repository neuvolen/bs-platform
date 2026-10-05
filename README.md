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
