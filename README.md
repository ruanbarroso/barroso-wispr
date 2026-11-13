# barroso-wispr

Live voice dictation inspired by [Wispr Flow](https://wisprflow.ai): hold a
hotkey, speak, release, and the text is pasted wherever your cursor is.

Transcription is live: audio streams to the
[Gemini Live API](https://ai.google.dev/api/live.md) while you hold the key,
and a short cleanup pass fixes the spelling of your own terms (names, companies,
jargon), removes filler words, punctuates, and keeps only the final version
when you correct yourself mid-sentence. The cleanup treats what you dictated as
text to clean, never as instructions: if you dictate a question or a command,
it is pasted as you said it, not answered or executed. If the cleanup fails or
answers instead of cleaning, the raw transcription is pasted.

Status: early MVP for Windows and macOS.

## Usage

- **Hold Left Ctrl + Left Alt** (Control + Option on macOS) to dictate; release
  to paste the text at the cursor.
- **Double-tap** the combo to latch recording hands-free; tap once to stop.
- A **triple tap** is ignored: nothing is recorded.
- AltGr (Right Alt) and Ctrl+Alt+other keys never trigger it, so ABNT2 and
  other layouts that use AltGr keep working. Pressing another key while
  recording cancels the dictation.
- Recordings last up to 15 minutes. A recording shorter than 1 second, or one
  where no speech is detected (local voice activity detection plus a check that
  the level varies like speech, so steady noise does not count), is discarded
  without transcribing or pasting anything.
- If you start a new dictation before the previous one was pasted, the texts
  are pasted together, in the order you spoke them.
- Only one copy of the app listens for the hotkey: starting a newer copy closes
  the older one.

### Indicators

- **Recording:** a pulsing blue border (#238BFF) around the desktop, which does
  not intercept mouse input, and an entry chime. Both appear as soon as the
  Live session is open, before you speak, so you know you can start talking.
- **Transcribing** (between release and paste): the border turns green (#34C759)
  and a soft 440 Hz pulse plays. Starting a new recording turns it blue again.
- **End of recording:** a descending two-note chime. If the connection drops
  while you talk, recording stops, a lower three-note chime plays, and what was
  already transcribed is pasted.

## Windows

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

On Windows the text is pasted with Ctrl+V. The previous clipboard content is
saved and restored after pasting, unless you copied something else in the
meantime. If pasting fails, the app types the text with Unicode key events.

### Automatic updates

On Windows and macOS the app checks the latest stable release on GitHub at
startup and then about once every 24 hours. A verified update downloads in the
background and is applied only after dictation, final transcription, cleanup,
and paste have finished. On Windows the logon task runs elevated, allowing its
updater helper to replace the executable under Program Files without another
UAC prompt; on macOS the installed app is replaced without a password. Wispr
then restarts with the same arguments, working directory, and environment;
recording is briefly unavailable during this handoff.

Release checks and downloads use the network but failures do not interrupt
dictation. `dev` builds and unsupported architectures skip updates. The updater
uses each release's SHA-256 and candidate `-version` checks before replacing the
executable, keeping a numbered backup for rollback. User configuration is never
part of an update.

## macOS

The release contains standalone binaries for Apple silicon (`arm64`) and Intel
(`amd64`). Download the matching `barroso-wispr-darwin-*` file, then in Terminal
run (replace the filename if using an Intel Mac):

```sh
chmod +x ./barroso-wispr-darwin-arm64
# Only needed for a download quarantined by your browser:
xattr -d com.apple.quarantine ./barroso-wispr-darwin-arm64
export WISPR_API_KEY="your-google-ai-studio-key" # or your bk- Barroso Keys key
./barroso-wispr-darwin-arm64 --install
```

The installer creates `~/Applications/Barroso Wispr.app`, registers it to start
at every login, and starts it now. It does not require an administrator password.
On first use, macOS asks for permission to access the microphone, monitor input,
and control the computer. If a prompt was dismissed, grant access in **System
Settings → Privacy & Security → Microphone**, **Input Monitoring**, and
**Accessibility**. Quit and reopen the app after changing these permissions.

The app runs without a Dock icon. The installer tries to create a stable local
signing identity so macOS can retain permissions across updates. If local
signing is unavailable, it falls back to ad-hoc signing and macOS may ask for
the permissions again after an update. Do not run `--install` with `sudo`.

The text is pasted with Command+V through the pasteboard, which is marked
transient so clipboard managers ignore it. The previous pasteboard content is
**not** restored: restoring it made Chrome paste empty text from the second
dictation on. After pasting, the app reads the focused field through the
Accessibility API and logs whether the insertion was confirmed. If pasting
fails, it types the text with Unicode key events.

Set `WISPR_API_KEY` in your shell before installing; the installer copies its
value into the private (0600) LaunchAgent configuration so the app can access
the key after login. Keep that plist private and reinstall after changing the
key. For a one-off foreground run without installing, run the binary directly
from Terminal after setting the variable in that shell. The LaunchAgent is
`tec.barroso.wispr`; Go panics are written to `~/.barroso-wispr/stderr.log`.

## Credential setup

Set exactly one environment variable: `WISPR_API_KEY`. The backend is selected
from the key prefix; there is no URL or provider setting:

- Keys starting with `bk-` use `https://keys.barroso.tec.br/v1`, voice model
  `barroso-audio-stream` (the gateway selects the real chain model via
  `x-served-model`), cleanup model `barroso-quick` and fallback transcription
  model `barroso-stt`.
- Any other key is sent to Google AI Studio. Create a key at
  <https://aistudio.google.com/apikey>. Voice is fixed to
  `gemini-3.5-transcribe-live`; cleanup and fallback transcription are fixed to
  `gemini-3.5-flash-lite`. The app mints a one-use
  [ephemeral token](https://ai.google.dev/gemini-api/docs/ephemeral-tokens)
  for each dictation.

The app requests the next Live token in advance, at startup and after each
dictation, with up to three parallel attempts when the gateway is slow, so the
session opens quickly when you press the hotkey. If the Live session returns no
text, the whole recording is transcribed in one request with the fallback model.

```powershell
[Environment]::SetEnvironmentVariable("WISPR_API_KEY", "your-google-ai-studio-key", "User")
# or: [Environment]::SetEnvironmentVariable("WISPR_API_KEY", "bk-your-barroso-keys-key", "User")
```

Sign out and back in (or reopen the terminal) after setting the user variable.
For a one-off foreground run only, `$env:WISPR_API_KEY = "..."` also works, but
a process-only value is not inherited by the scheduled logon task.

The Google Live transcription model was chosen from the tested candidates: it
has free-tier input and output listed by Google and gave the best transcription
accuracy in our 17-second pt-BR comparison. Google may change its free-tier
availability and rate limits; see [pricing](https://ai.google.dev/gemini-api/docs/pricing).

## Preferences

The only setting in `~/.barroso-wispr/config.json` is an optional dictionary
path. It contains no provider, key, URL, or model selection.

| key           | meaning                                         |
|---------------|-------------------------------------------------|
| `dictionary` | optional path to the dictionary file |

## Dictionary

The dictionary path defaults to `~/.barroso-wispr/dicionario.txt`, created with
a comment header on first start: one term per line, `#` for comments. These
terms are hinted to the live session and enforced, with exact spelling, in the
cleanup pass. The file is read at each dictation, so edits apply without
restarting.

## Files

Everything lives in `~/.barroso-wispr` (override with `BARROSO_WISPR_HOME`):
`config.json`, `dicionario.txt`, `wispr.log` (session timings, discards, paste
diagnostics and the final text of each dictation), `update/` (downloaded updates) and,
on macOS, `stderr.log`.

## Build

```powershell
$env:CGO_ENABLED = "0"
go build -ldflags "-H windowsgui" -o barroso-wispr.exe ./cmd/barroso-wispr
```

```sh
CGO_ENABLED=0 go build -o barroso-wispr ./cmd/barroso-wispr   # macOS
```

Go 1.26, no CGO (macOS uses [purego](https://github.com/ebitengine/purego)).
The common files carry `//go:build windows || darwin`, so on other systems run
`GOOS=darwin go vet ./...` or `GOOS=windows go vet ./...`.

Flags:

- `--version`: print the version.
- `--install`: install and start the app (see above).
- `-arquivo audio.pcm`: dictate a raw s16le, 16 kHz, mono file and print the
  text, without microphone or keyboard.
- macOS only: `--testar-colar "texto"` pastes the text into the focused app
  after 2 s; `--simular-atalho "frase"` holds the hotkey and speaks the phrase
  with `say`; `BW_DIAG_MIC_PCM=<file.pcm>` replaces the microphone with a file
  in the normal flow (use a temporary `BARROSO_WISPR_HOME`).

## Releases

Each push to `main` is built by the deployer (`.deploy/release.json`) for
Windows (amd64, arm64) and macOS (arm64, amd64) and published as a
`vX.Y.Z-bw.N` release, which the auto-updater picks up.

## License

[MIT](LICENSE)
