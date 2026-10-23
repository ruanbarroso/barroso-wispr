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

## Credential setup

Set exactly one environment variable: `WISPR_API_KEY`. The backend is selected
from the key prefix; there is no URL or provider setting:

- Keys starting with `bk_` use `https://keys.barroso.tec.br/v1`, voice model
  `barroso-audio-stream` (the gateway selects the real chain model via
  `x-served-model`) and cleanup model `barroso-quick`.
- Any other key is sent to Google AI Studio. Create a key at
  <https://aistudio.google.com/apikey>. Voice is fixed to
  `gemini-3.5-transcribe-live`; cleanup is fixed to `gemini-3.5-flash-lite`.
  The app mints a one-use [ephemeral token](https://ai.google.dev/gemini-api/docs/ephemeral-tokens)
  for each dictation.

```powershell
$env:WISPR_API_KEY = "your-google-ai-studio-key"
# or: $env:WISPR_API_KEY = "bk_your-barroso-keys-key"
```

The Google Live transcription model was chosen from the tested candidates: it
has free-tier input and output listed by Google and gave the best transcription
accuracy in our 17-second pt-BR comparison. Google may change its free-tier
availability and rate limits; see [pricing](https://ai.google.dev/gemini-api/docs/pricing).

When the Live session is ready to receive speech, the app plays a ready beep.
When capture ends, it plays a second beep before final transcription and paste.

## Preferences

The only setting in `~/.barroso-wispr/config.json` is an optional dictionary
path. It contains no provider, key, URL, or model selection.

| key           | meaning                                         |
|---------------|-------------------------------------------------|
| `dictionary` | optional path to the dictionary file |

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
