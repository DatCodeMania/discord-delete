<div align="center">

# discord-delete

Bulk-delete your Discord messages and reactions, driven by your GDPR data package.

[![Release](https://img.shields.io/github/v/release/DatCodeMania/discord-delete)](https://github.com/DatCodeMania/discord-delete/releases)
[![CI](https://github.com/DatCodeMania/discord-delete/actions/workflows/ci.yml/badge.svg)](https://github.com/DatCodeMania/discord-delete/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<img src="demo/demo.gif" alt="discord-delete demo" width="640">

</div>

<br>

discord-delete reads the exact channel and message IDs from your data package so it only sends DELETEs. The search API is never touched, and nothing breaks on a UI change. It removes messages and reactions, headless or through an interactive terminal UI (TUI).

> [!TIP]
> Try discord-delete using a [demo package](https://github.com/DatCodeMania/discord-delete/raw/main/demo/demo-package.zip). It's fake data so no token/account is needed and nothing is deleted.

> [!WARNING]
> Automating a user account is against Discord's Terms of Service and can get it banned. While this tool cooperates with Discord's rate limits, that doesn't fully protect against a ban.

## Install

- **AUR** (Arch)

  Any AUR helper for `discord-delete-bin`:

  ```sh
  paru -S discord-delete-bin   # or: yay -S discord-delete-bin
  ```

- **apt** (Debian, Ubuntu, Mint)

  ```sh
  wget https://github.com/DatCodeMania/discord-delete/releases/latest/download/discord-delete_linux_amd64.deb && sudo apt install ./discord-delete_linux_amd64.deb && rm discord-delete_linux_amd64.deb
  ```

- **dnf** (Fedora, RHEL, Rocky, Alma)

  ```sh
  sudo dnf install https://github.com/DatCodeMania/discord-delete/releases/latest/download/discord-delete_linux_amd64.rpm
  ```

  Swap `amd64` for `arm64` on ARM. Alpine `.apk` in [releases](https://github.com/DatCodeMania/discord-delete/releases).

- **Homebrew** (macOS)

  ```sh
  brew install DatCodeMania/tap/discord-delete
  ```

- **Scoop** (Windows)

  ```sh
  scoop bucket add datcodemania https://github.com/DatCodeMania/scoop-bucket
  scoop install discord-delete
  ```

- **Binary releases** (any platform)

  Download the official binaries from the [releases page](https://github.com/DatCodeMania/discord-delete/releases). Each archive contains a launcher which you can use to launch, by double-clicking from a file manager.

- **Go** (1.26+)

  ```sh
  go install github.com/DatCodeMania/discord-delete@latest
  ```

<details>
<summary>Build from source</summary>

```sh
git clone https://github.com/DatCodeMania/discord-delete
cd discord-delete
go build -o discord-delete .
```

</details>

<details>
<summary>macOS/Windows unsigned binaries</summary>

The binaries are not code-signed. On macOS, a manually downloaded binary may be blocked by Gatekeeper on first run: choose Open from the right-click menu, or run `xattr -dr com.apple.quarantine ./discord-delete` (Homebrew does this for you). On Windows, SmartScreen may warn: click More info, then Run anyway.

</details>

## Usage

Obtain your data package: in Discord, go to Settings > Data & Privacy > Request Data and enable both Messages and Activity (Activity for reactions). Discord emails it within a few hours to days.

By default every run is a dry run: it previews what would be deleted and changes nothing. Add `--execute` (or confirm in the TUI) for a proper run.

Then point discord-delete at your package:

- **TUI**

  ```sh
  discord-delete                          # default, with the package picker
  discord-delete --package package.zip    # without
  ```

  During a run: `space` or `p` pauses and resumes, `[` and `]` nudge the pacing floor slower or faster by 0.2s per step, `q` quits.

- **Headless**

  ```sh
  discord-delete --package package.zip --no-tui                               # dry run
  DISCORD_TOKEN=... discord-delete --package package.zip --no-tui --execute   # execute, default config
  ```

Full help: `discord-delete --help`

## Flags

<details>
<summary>All flags</summary>

**Select what to delete**

| Flag | What it does |
|---|---|
| `--content` | Only messages whose text contains this substring, or a regex in slashes: `/pattern/` or `/pattern/i` |
| `--type` | Comma-separated types: `text`, `media`, `image`, `video`, `audio`, `voice`, `file`, `link` |
| `--after-date` / `--before-date` | Only messages after / before this date (YYYY-MM-DD or RFC3339) |
| `--last` | Only messages within a recent window: `7d`, `2w`, `3mo`, `1y`, `day`/`week`/`month`/`year`, or `today` |
| `--after` / `--before` | Only messages after / before this message ID |
| `--order` | Deletion order per channel: `oldest` (default) or `newest` |
| `--guild` | Delete only these comma-separated guild IDs |
| `--channel` | Delete only these comma-separated channel IDs |

**Run mode**

| Flag | What it does |
|---|---|
| `--package` | Path to the `.zip` or extracted folder |
| `--no-tui` | Plain non-interactive run |
| `--execute` | Start in execute mode (real deletion) instead of a dry run |
| `--version` | Print version and exit |

**Reactions**

| Flag | What it does |
|---|---|
| `--reactions` | Also remove your reactions |
| `--no-messages` | Skip message deletion (reactions only) |
| `--run-order` | Which phase runs first when doing both: `messages` (default) or `reactions` |
| `--reaction-channel` | Comma-separated channel IDs to scope reaction removal |
| `--reaction-delay` | Base seconds between reaction removals within one channel (default 0.3) |

**Pacing**

| Flag | What it does |
|---|---|
| `--delay` | Base seconds between message deletes within one channel (default 1.1) |
| `--jitter` | +/- jitter fraction applied to `--delay` (default 0.4) |
| `--workers` | Channels deleted concurrently (default 4) |
| `--max-rps` | Hard account-wide request/sec ceiling (default 25) |

**Notifications and report**

| Flag | What it does |
|---|---|
| `--ntfy` | ntfy topic or full URL to notify (or set `DISCORD_DELETE_NTFY`) |
| `--notify-every` | Progress push interval during a run: `30m`, `1h`, or `off` (default `30m`) |
| `--report` | Write the end-of-run report to this path |

**Token**

| Flag | What it does |
|---|---|
| `--token` | User token |
| `--forget-token` | Delete any stored token for this package's account, then exit |

**Environment**

| Variable | What it does |
|---|---|
| `DISCORD_TOKEN` | Your token |
| `DISCORD_DELETE_NTFY` | Default ntfy topic or URL, same as `--ntfy` |
| `DISCORD_DELETE_STATE_DIR` | Where the resume log, saved TUI settings, reports, and update cache live (default: your OS cache dir) |
| `DISCORD_DELETE_CHROME` | Path to a Chromium-based binary, if browser sign-in cannot find one |
| `DISCORD_DELETE_NO_UPDATE_CHECK` | Set to anything to skip the daily update check |

</details>

## Unattended runs

Deleting messages across a large account takes days. Older messages hit a shared, account-wide limit that caps deletion at roughly one every two seconds, so my own 400,000+ message account, the largest tested so far, took over a week. discord-delete can be run headless on a server without any babysitting. Configure a [ntfy](https://ntfy.sh) topic (`--ntfy`) to receive:

- a ping when the run finishes
- progress every `--notify-every` (default `30m`): percent done, deleted, skipped, and failed counts, ETA, and the latest error if any
- **Pause**, **Resume**, and **Stop** buttons you tap from your phone, which steer the running job through a companion topic (your topic plus `-ctl`)

```sh
DISCORD_TOKEN=... discord-delete --package package.zip --no-tui --execute --ntfy my-topic
```

> [!NOTE]
> ntfy.sh topics are publicly viewable by default. Notifications carry only counts and status, no message content or channel names. Anyone who can read your topic can pause/stop the run, so use a hard-to-guess topic, or your own ntfy server.

## Reports

Every run (including dry) writes a plain-text report when it ends: finished, stopped with `q`, or aborted.

It holds the run's mode and status, how long it took, and per phase how many items were deleted, skipped, failed, and how many were already gone from an earlier run. Then it lists everything Discord refused to delete (403), grouped by server, with Discord's own reason and whether you are still a member.

```
Undeletable messages (403), grouped, with the reason Discord returned:
  Some Server                   1,204  in 18 channel(s)  (you left this server)
      reason: Missing Access
  #weekend crew                     9  channel 1046829174553219884
      reason: Cannot execute action on a system message
```

`Missing Access` means you lost access to that server or DM, so rejoin or reopen it and run again: a re-run retries what was skipped and skips what is already deleted.

System messages: Your package includes the ones you triggered (starting a call, renaming a group, adding or removing someone), and these are not deletable. They look like normal messages until Discord rejects the delete.

Reports land in your OS cache directory, timestamped, next to the resume log:

```
~/.cache/discord-delete/progress/user-<id>-20260717-143022.report.txt   # Linux
~/Library/Caches/discord-delete/progress/...                            # macOS
%LocalAppData%\discord-delete\progress\...                              # Windows
```

Use `--report PATH` to write it somewhere specific, or `DISCORD_DELETE_STATE_DIR` to move the whole directory.

When a run completes, that path is printed with a button. Click it or press `o` to open the report. The path is also directly ctrl-clickable (cmd on macOS) in some terminals which support OSC 8 hyperlinks.

That directory also holds the resume log, `<key>.deleted.log`, one confirmed-gone ID per line. It is keyed by account and therefore works across packages. Delete the log to start over from scratch.

The TUI also saves its settings to `<key>.config.json` in that directory, so the next run for that account starts where you left off. The token is not saved here. Any explicit flag overrides a saved value, and headless runs ignore the file.

## Reactions

discord-delete also removes reactions you added, reading them from the Activity data in your package. They run as a separate phase, set up in the TUI like everything else.

Headless, enable them with `--reactions` (messages first by default), or run them on their own:

```sh
discord-delete --package package.zip --reactions                        # messages, then reactions
discord-delete --package package.zip --no-messages                      # reactions only
discord-delete --package package.zip --reactions --run-order reactions  # reactions first

# reactions only, headless, scoped to two channels at a slower per-channel pace:
discord-delete --package package.zip --no-messages --no-tui --reaction-channel 1097843025518923776,1099156482310307840 --reaction-delay 1.5
```

## How it works

Your data package holds the exact channel and message IDs for everything you posted, so discord-delete never has to search. It sends one DELETE per item to Discord's REST API, the same endpoint the client uses, and reads the response to decide what to do next.

Discord's rate-limit buckets are per channel, so each channel is cleared serially at roughly human pace, many in parallel. Older messages share one account-wide bucket, so the run reduces to one worker. Pacing is adaptive per channel (AIMD off a floor), so a run settles on the fastest rate Discord tolerates. A 429 only means slow down: it pauses every worker without aborting. Only a sustained wall of 401s (which is probably a dead token) aborts.

Every confirmed-gone ID (deleted or already absent) is written to a log as it happens. A re-run filters those out before queuing anything, so a crash or exit loses no progress.

<details>
<summary>More reliability detail</summary>

- Additive increase/multiplicative decrease (AIMD) pacing: `--delay` is a floor, not a fixed rate. Each channel widens its spacing multiplicatively when throttled and tightens it additively as deletes succeed, down to the floor.
- Rate-limit header: It reads Discord's `X-RateLimit-*` headers: once a bucket reports no calls left, that worker waits out the reset before its next request in that bucket.
- Account-wide ceiling: A hard requests-per-second cap (default 25/s, half Discord's ~50/s limit) and a minimum interval between calls cap total volume, no matter how many channels run.
- Auto-collapse: Deleting messages older than ~two weeks hits a shared, account-wide limit where parallel workers only produce redundant 429s, so the engine drops to a single worker for the rest of the run once it detects this.
- Status codes: 404 (already gone) counts as done. A 403 is skipped: a system message is undeletable by anyone, so it is recorded and never asked about again, while lost access stays retryable because rejoining fixes it.
- Cloudflare budget: Discord temporarily bans an IP after roughly 10,000 invalid responses in ten minutes; that count is tracked so the run pauses before it reaches the cap.
- Formats: Reads the pre- and post-June-2025 package layouts, TitleCase and lowercase JSON keys, and the older CSV export.

</details>

## Your token

Deleting needs your user token (a dry run does not). Two ways to provide it:

- Browser sign-in: Opens a Chromium browser (Chrome, Edge, Brave, Chromium, Vivaldi, Opera, or Arc on macOS, auto-detected; set `DISCORD_DELETE_CHROME` if yours isn't found) at Discord's login, in a throwaway profile. Log in normally, 2FA included, and the tool lifts the token off the first authenticated request.
- Manual entry: Type it into the token field, or provide `DISCORD_TOKEN` or `--token`.

On Linux, a snap or Flatpak browser is used only if nothing else is installed. Those builds run with a private `/tmp` and cannot read the throwaway profile, so browser sign-in may fail with them. 

The token is then verified against Discord (`GET /users/@me`), and the tool shows who you are logged in as.

How it is handled:

- By default the token is never written to disk. It exists in memory only, and the only place it is sent is Discord itself, same as the official app.
- Storage is opt-in and keyring-only: Turn on **Remember token** and a validated token goes to your OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret Service), encrypted at rest. Without a keyring it stays in memory. `--forget-token` removes it, and an invalid token clears itself.

## Alternatives

The two most popular alternatives are below, both browser-based. A large account takes days on any of these; discord-delete is the one you start in a console and leave alone. Discrub is the closest comparison: actively maintained, with a well-designed web app for anyone not comfortable in a terminal. Pick whichever suits your use case.

| | discord-delete | [Undiscord](https://github.com/victornpb/undiscord) | [Discrub](https://github.com/prathercc/discrub) |
|---|---|---|---|
| **Type** | Standalone binary + TUI/CLI | Browser userscript | Browser extension / web app |
| **No browser needed** | ✅ | ❌ | ❌ |
| **Headless** | ✅ | ❌ | ❌ |
| **Notify + control from phone** | ✅ | ❌ | ❌ |
| **Deletion pacing** | Adaptive (AIMD), parallel where possible | Delays only increase on 429, serial | Fixed delays + jitter, serial |
| **Removes your reactions** | ✅ | ❌ | ✅ |
| **Works from your data package** | ✅ | ❌ (search API) | ✅ (or search API) |
| **Delete others' messages (w/ Manage Messages)** | ❌ | ✅ | ✅ |
| **Regex content filter** | ✅ | ✅ | ❌ |
| **License** | MIT | MIT | Source-visible |

## Limitations

- Discord takes a few hours to a few days to send the package. The only way to speed it up is to request less: Messages for messages, Activity for reactions.
- The package covers what existed when Discord built it. Anything posted after that is not in it, so if you ever intend to re-delete you must request a fresh one.
- discord-delete does not delete other people's messages, even where you have Manage Messages.

## License

MIT. See [LICENSE](LICENSE).
