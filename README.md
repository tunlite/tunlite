# Tunlite

> **SSH tunnels for you and your Agent — kept alive.** Type the `-L`/`-R`/`-D` yourself, or
> just tell an AI Agent — tunlite builds the tunnel and keeps it connected.

**English** · [简体中文](README.zh-CN.md)

[![CI](https://github.com/yuanyuanzijin/tunlite/actions/workflows/ci.yml/badge.svg)](https://github.com/yuanyuanzijin/tunlite/actions/workflows/ci.yml)
[![npm](https://img.shields.io/npm/v/tunlite)](https://www.npmjs.com/package/tunlite)
[![downloads](https://img.shields.io/npm/dm/tunlite)](https://www.npmjs.com/package/tunlite)
[![node](https://img.shields.io/badge/node-%E2%89%A518-brightgreen)](https://nodejs.org)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Keeping SSH tunnels alive is a chore: pros juggle `autossh`, a `systemd` unit per tunnel,
and a pile of `-L`/`-R`/`-D` flags — and reconnect by hand when one drops; newcomers don't
even know where to start. **tunlite** folds it into one command: type it yourself, or just
tell an **AI Agent** in plain words — either way it builds the tunnel, keeps it alive, and
reconnects on its own. Pure Node.js, **zero dependencies**, wrapping the `ssh` you already
trust.

<p align="center"><img src="https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/docs/demo.gif" alt="define a tunnel, the daemon brings it up, check status, tail logs" width="760"></p>

> 📖 **Full documentation → [tunlite.dev](https://tunlite.dev/)**

> 🧭 **v1 project direction → [docs/PROJECT_DIRECTION.md](docs/PROJECT_DIRECTION.md).**
> This is the sole source of truth for the SSH-focused Go v1, daemon removal,
> native service architecture, and the extension boundaries reserved for future
> drivers. The rest of this README documents the currently released 0.x product.

- **Agent-native** — `--json` + stable exit codes on every command, plus a bundled Agent skill: an AI Agent sets up, brings up, and troubleshoots tunnels end-to-end.
- **Zero third-party dependencies** — pure Node.js standard library; all it needs on the box is **Node ≥ 18** and the system `ssh` it wraps.
- **Auto-reconnect** — exponential backoff + jitter, keepalive, port health probes.
- **Start at login** — launchd (macOS) / systemd user service (Linux) / Task Scheduler (Windows — beta).
- **Passwordless setup** — connects directly if keys already work; installs your key only if needed.
- **Three forward types** — local `-L`, remote `-R`, dynamic SOCKS `-D`.

## For Agents

An **AI Agent** is a first-class user. Ask one in plain language and it drives `tunlite`
through the same `--json` surface you would — branching on exit codes, not scraping prose:

```text
you   ▸ "Forward the Postgres on app01 to my laptop."
Agent ▸ tunlite add pg --to deploy@app01 -L 5432:localhost:5432 --json   → {"ok":true,…}
Agent ▸ tunlite enable pg --json                                         → exit 4 · needs-auth
Agent ▸ tunlite setup-key deploy@app01                                   → key installed
Agent ▸ tunlite enable pg --json                                         → {"state":"connected"} · exit 0
Agent ▸ "Done — localhost:5432 reaches app01's Postgres; the daemon keeps it alive."
```

The bundled [`skill/ssh-tunnel`](skill/ssh-tunnel/SKILL.md) (installed by `tunlite install
skill`) tells an Agent exactly how: `--json`, branching on exit codes, and handling
`needs-auth`.

`tunlite monitor` gives you a live, top-style dashboard — every tunnel's state at a
glance, with the daemon auto-reconnecting a dropped one in front of you:

<p align="center"><img src="https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/docs/monitor.gif" alt="tunlite monitor — live dashboard with auto-reconnect and per-tunnel detail" width="760"></p>

## Why tunlite?

If you keep a few SSH tunnels running — a reverse tunnel to a homelab box, a SOCKS
proxy through a bastion, a port-forward to a staging database — you've probably wired
up `autossh` plus a `systemd`/`launchd` unit for each, and memorized which
`-L`/`-R`/`-D` flag goes where. tunlite folds all of that into one declarative CLI on
top of the `ssh` you already trust: named tunnels a daemon keeps alive and the OS
restarts at boot — no new server, no account, no protocol. And because every command
is `--json` with stable exit codes, an Agent drives the exact same surface you do.

| | tunlite | autossh | plain `ssh -L/-R/-D` | sshuttle | frp · bore · chisel | ngrok |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| Agent-friendly (`--json`, stable exit codes) | ✅ | ❌ | ❌ | ❌ | ❌ | partial |
| Wraps your system `ssh` (keys, jump hosts, `ssh_config`) | ✅ | ✅ | ✅ | partial | ❌ own protocol | ❌ own service |
| Named, declarative tunnels | ✅ | ❌ | ❌ | ❌ | ✅ config | ✅ |
| Auto-reconnect (backoff, keepalive, health) | ✅ | basic | ❌ | ❌ | ✅ | ✅ |
| Start at login (launchd/systemd/Task Scheduler) | ✅ | DIY | DIY | DIY | DIY | ✅ |
| Local **+** remote **+** dynamic SOCKS | ✅ | ✅ | ✅ | transparent proxy | varies | varies |
| Zero deps · no server to run · self-hosted | ✅ | needs autossh | ✅ | needs python | needs a server | hosted/paid |

## Install

Prerequisite: **Node ≥ 18** and the system `ssh`, both on your PATH.

```bash
# Recommended — fetch + anchor (no global npm needed)
npx tunlite install

# Or a curl one-liner (just curl/wget + tar + node)
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh

# Windows (PowerShell) — beta
irm https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.ps1 | iex
```

`tunlite install` copies the runtime to a fixed directory and writes a launcher that
**pins node's absolute path** (so switching nvm/fnm versions won't break it), then asks
whether to register login autostart, install the Agent skill, and enable shell
completion. Pass `-y` to say yes to all without prompting (for scripts/CI); with no
`-y` and no terminal it just anchors. To set up one piece on its own, use
`tunlite install service` / `install skill` / `install completion`. It also writes a
short `tun` alias when that name is free. **Windows (autostart, launcher, PATH) is
beta** — macOS/Linux are the CI-tested platforms.

## Quick start

```bash
# ssh-native forward flags (repeatable — one tunnel can carry several):
tunlite add web   --to me@host -L 8080:localhost:80   # reach the server's :80 at localhost:8080
tunlite add rev   --to me@host -R 9000:localhost:3000 # expose local 3000 as server:9000
tunlite add socks --to me@host -D 1080                # SOCKS5 proxy (local 1080)

tunlite status             # aligned table: NAME STATE HOST TYPE ROUTE PID UP RESTARTS
tunlite logs web -f        # follow logs
tunlite doctor             # health check: why a tunnel won't connect
```

> **Upgrading from 0.9.x?** 0.10.0 leans into native `ssh`, so a couple of commands take a
> new shape — while your existing tunnel config keeps working, untouched.
> - **Forwards now speak ssh's own flags** — `add web --to me@host -L 8080:localhost:80 -D 1080`
>   (repeatable; `set <name>` edits them in place). The earlier `add local/remote/dynamic`
>   form gives way to this.
> - **Switching a tunnel on and off is now `enable` / `disable`** (it was `up`/`down`), each
>   naming what it acts on — a name, `--tag <label>`, or `all`. A retired or mistyped verb
>   gently points you to the right one (`tunlite up` → "did you mean `enable`?").
>
> Run `tunlite update` (or `npx tunlite@latest install`), then `tunlite --version` to confirm.

When the target isn't passwordless yet, running `tunlite enable <name>` in a terminal prompts for
the password once and installs your key. Or do it explicitly: `tunlite check user@server`
(exit 0 = already passwordless) / `tunlite setup-key user@server`.

**Autostart (optional):** `tunlite install service` registers the daemon to start at
login (and restart on crash). It also starts everything right away, so it *replaces* `enable`
when you want tunnels up persistently — you don't need both.

## Update

```sh
tunlite update              # upgrade to the latest (restarts the daemon; tunnels blip ~1s)
tunlite update v0.9.0       # install / roll back to a specific tag
tunlite update --check      # compare current vs latest only; change nothing
```

`update` upgrades to the **latest release tag** — it fetches that tag's tarball from GitHub
and re-anchors in place (**no npm, no git**), so `npx` installs the first copy and `update`
keeps it current at a real published version. It only self-updates an anchored install: from
a git checkout it points you to `git pull`, and from an `npm i -g` install to
`npm i -g tunlite@latest` (so that channel's version stays authoritative).

## Commands

```
add <name> -L/-R/-D …      define a tunnel        set / rm / rename     edit / delete / rename
list [--tag T]             list tunnels           run --to … -L/-R/-D …   daemon-less foreground tunnel
enable / disable / restart control (name|--tag|all)
status / logs / monitor    inspect (table · follow · live dashboard)
doctor                     why a tunnel won't connect
check / setup-key          probe / install passwordless access
webhook …                  drop alerts to a webhook (generic · WeCom)
export / import            back up / merge tunnels
install [service|skill|completion] / uninstall      anchor runtime · autostart · Agent skill · Tab-completion
update                     self-update from GitHub
```

Run `tunlite help` or any command with `--help` for full flags, or see the
[documentation](https://tunlite.dev/) for jump hosts (`--jump`),
tags (`--tag`), the webhook channels/events, and shell completion.

**Forwarding model:** forwards use the standard ssh flags, and they're repeatable — one
tunnel can carry several:
- `-L [bind:]PORT:HOST:HOSTPORT` — **local forward**: reach a remote service on your machine.
- `-R [bind:]PORT:HOST:HOSTPORT` — **remote forward**: expose a local service on the server.
- `-D [bind:]PORT` — **dynamic**: a local SOCKS5 proxy.

The optional `bind:` prefix is the listen address — default loopback; use `0.0.0.0` to
expose the listener to your LAN. Bracket IPv6 addresses (`[::1]`). The SSH port goes on the
target (`--to user@host:2222`, default 22). Editing a tunnel's forwards is `set <name>`:
passing any `-L/-R/-D` **replaces the whole forward set** (`set` is the sole forward editor).

**Exit codes** (add `--json` to any command): `0` ok · `2` usage · `3` not found ·
`4` needs key · `5` can't reach daemon · `1` other.

## How it works

Three roles, each with one job:

| Role | What it is | Job |
|---|---|---|
| **CLI** (`tunlite …`) | the commands you type | Edit `config.json`, talk to the daemon, run one-shot ssh. Exits when done. |
| **daemon** (`tunlite daemon run`) | a background process | Keeps tunnels connected, reconnects on drop, serves status/logs. |
| **service** (`install service`) | a launchd/systemd/Task Scheduler entry | Keeps the **daemon** alive — starts it at boot, restarts on crash. |

`config.json` is the single source of truth. The OS service keeps the daemon alive, the
daemon keeps every tunnel alive. Day to day you only need `add` → `enable` → `status`/`logs`,
plus `install service` once for autostart.

## Daemon-less: `run`

For containers and `systemd` entrypoints where a background daemon doesn't fit, `run`
supervises one tunnel in the **foreground** (auto-reconnect, keepalive) and stays attached
until you stop it — no daemon, no `config.json` entry:

```sh
tunlite run --to me@host -L 8080:localhost:80
tunlite run --to me@host -R 9000:localhost:3000 --name rev --json --exit-on-failure
```

`--name` labels the tunnel for status lines (defaults to the target host). `--json` emits NDJSON state lines on
stdout (one JSON object per state change). `--exit-on-failure` exits non-zero instead of
retrying — `needs-auth` → `4`, `blocked`/`failed` → `1` — so a supervisor restarts it.

## Versioning & license

SemVer (`vMAJOR.MINOR.PATCH`); release notes in [`CHANGELOG.md`](https://github.com/yuanyuanzijin/tunlite/blob/master/CHANGELOG.md).
MIT.
