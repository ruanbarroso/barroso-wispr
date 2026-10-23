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

Run `barroso-wispr.exe --install` **as Administrator** to install the app under
`%ProgramFiles%\Barroso Wispr`, register it to start at your interactive Windows
logon, and launch it immediately. If the command is not elevated, installation
fails before making changes and explains how to rerun PowerShell as Administrator;
the app never elevates itself. The scheduled task runs for your user at sign-in
with highest privileges and stores no password. The global hotkey is available
after you sign in, not while the PC is at the lock screen. Running elevated also
allows the hotkey to type into elevated applications.

Installation and upgrades do not move or remove your data. Preferences,
dictionary, and logs remain in your existing user profile at
`~/.barroso-wispr`. For the scheduled logon task, save `WISPR_API_KEY` as a
persistent **User** environment variable (not only `$env:WISPR_API_KEY` in a
single PowerShell process), so the task can read the same credential after
sign-in. The app continues to read the key from the installing user's
environment and never stores it in its config or task definition.

### Automatic updates

The Windows app checks the latest stable release on GitHub at startup and then
about once every 24 hours. A verified update downloads in the background and is
applied only after dictation, final transcription, cleanup, and paste have
finished. The logon task runs elevated, allowing its updater helper to replace
the executable under Program Files without another UAC prompt. Wispr then
restarts with the same arguments, working directory, and environment; recording
is briefly unavailable during this handoff.

Release checks and downloads use the network but failures do not interrupt
dictation. `dev` builds and unsupported architectures skip updates. The updater
uses each release's SHA-256 and candidate `-version` checks before replacing the
executable, keeping a numbered backup for rollback. User configuration is never
part of an update.

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
[Environment]::SetEnvironmentVariable("WISPR_API_KEY", "your-google-ai-studio-key", "User")
# or: [Environment]::SetEnvironmentVariable("WISPR_API_KEY", "bk_your-barroso-keys-key", "User")
```

Sign out and back in (or reopen the terminal) after setting the user variable.
For a one-off foreground run only, `$env:WISPR_API_KEY = "..."` also works, but
a process-only value is not inherited by the scheduled logon task.

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

```powershell
$env:CGO_ENABLED = "0"
go build -ldflags "-H windowsgui" -o barroso-wispr.exe ./cmd/barroso-wispr
```

Go 1.26, no CGO. Diagnostics without microphone or keyboard:
`barroso-wispr.exe -arquivo audio.pcm` (raw s16le, 16 kHz, mono).

## License

[MIT](LICENSE)
