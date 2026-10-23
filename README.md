# barroso-wispr

Live voice dictation inspired by [Wispr Flow](https://wisprflow.ai): hold a
hotkey, speak, release, and the text is typed wherever your cursor is.

Transcription is live: audio streams to the
[Gemini Live API](https://ai.google.dev/api/live.md) while you hold the key,
and a short cleanup pass fixes the spelling of your own terms (names, companies,
jargon), removes filler words, punctuates, and keeps only the final version
when you correct yourself mid-sentence.

Status: early MVP, **Windows only** for now (macOS and Linux are planned).

## Usage

- **Hold Left Ctrl + Left Alt** to dictate; release to type the text.
- **Double-tap** the combo to latch recording hands-free; tap once to stop.
- AltGr (Right Alt) and Ctrl+Alt+other keys never trigger it, so ABNT2 and
  other layouts that use AltGr keep working.

Run `barroso-wispr.exe` and leave it open. Logs go to the terminal and to
`~/.barroso-wispr/wispr.log`. It cannot type into windows running as
administrator unless it also runs elevated.

## Providers

`barroso-wispr` works with either backend. Set `provider` in
`~/.barroso-wispr/config.json`, or leave it empty for automatic selection
(barroso-keys if its credentials exist, otherwise Google AI Studio).

### Google AI Studio (direct)

```json
{ "provider": "google" }
```

Key from `google_api_key` in the config, `GEMINI_API_KEY`, or `GOOGLE_API_KEY`
(get one at <https://aistudio.google.com/apikey>). A one-use
[ephemeral token](https://ai.google.dev/gemini-api/docs/ephemeral-tokens) is
minted per dictation; cleanup uses Gemini's OpenAI-compatible endpoint.
Defaults: `gemini-3.5-transcribe-live` for voice, `gemini-3.5-flash-lite` for
cleanup.

### barroso-keys gateway

```json
{ "provider": "barroso" }
```

Credentials from `BARROSO_BASE_URL` / `BARROSO_AUTH_TOKEN`, or the
keys.barroso.tec.br provider in the Barroso Harness config
(`~/.barroso-harness/config.json`). Voice uses the `barroso-audio-stream`
virtual model (fallback chain, real model in `x-served-model`); cleanup uses
`barroso-quick`.

### Other options

| key           | meaning                                         |
|---------------|-------------------------------------------------|
| `model`       | voice model (virtual on barroso, real on google) |
| `quick_model` | cleanup model                                   |
| `dictionary`  | path to the dictionary file                     |

## Dictionary

`~/.barroso-wispr/dictionary` path defaults to `~/.barroso-wispr/dicionario.txt`:
one term per line, `#` for comments. These terms are hinted to the live
session and enforced, with exact spelling, in the cleanup pass.

## Build

```
CGO_ENABLED=0 go build -o barroso-wispr.exe ./cmd/barroso-wispr
```

Go 1.26, no CGO. Diagnostics without microphone or keyboard:
`barroso-wispr.exe -arquivo audio.pcm` (raw s16le, 16 kHz, mono).

## License

[MIT](LICENSE)
