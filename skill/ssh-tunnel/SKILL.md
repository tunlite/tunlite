---
name: ssh-tunnel
description: Use when you need to create, manage, inspect, or troubleshoot SSH tunnels / port-forwards on this machine through the `tunlite` CLI — local (-L), remote (-R), or dynamic SOCKS (-D) forwards, auto-reconnecting tunnels supervised by a daemon, registering that daemon to start at login, and setting up passwordless (key-based) access to a target host. Trigger when a task says things like "forward a port over SSH", "keep an SSH tunnel alive", "open a SOCKS proxy through host X", "make a reverse tunnel", "start my tunnels on boot", or "set up passwordless SSH to host Y".
---

# Driving `tunlite` (SSH tunnel manager)

`tunlite` defines named SSH tunnels in a config file and supervises them with a
background daemon that auto-reconnects and can be registered to OS startup.
**Always pass `--json`** for machine-readable output, and **branch on the exit
code** rather than parsing prose.

## Step 0 — get `tunlite`, then just use it

There is **one** operational tunlite: an *anchored* install (a stable copy with a pinned
node path). npm, the curl bootstrap, and any copy bundled with this skill are all just
ways to **seed** that one install — not separate tools to choose between. So, once, up front:

1. Run `tunlite --version`.
   - **`0.10.0` or newer** → you're set.
   - **`command not found`, or `0.9.x` or earlier** (missing, or below this skill's floor
     of **≥ 0.10.0** — the ssh-native `-L`/`-R`/`-D` + `run` + `set` surface): **seed it,
     and tell the user — never install silently.** If this skill bundle ships a copy, run
     `node <this-skill-dir>/bin/tunlite.js install`; otherwise `npx tunlite install`. Both
     anchor a stable copy. (Installed but merely old → `tunlite update`.)
2. **After step 1, always just call `tunlite …`** — it resolves to the anchored copy. Stop
   deciding between `npx` / a bundled path / PATH; there is one tunlite now.
3. **Persistence is one extra line — and you must offer it, not wait to be asked.**
   A tunnel stays up only while the daemon does, and a bare `tunlite install` does
   **not** register autostart (nor does `enable` — it only starts the daemon on
   demand). So whenever a tunnel is meant to last, **proactively ask the user
   whether it should keep running across reboots / logins.** If yes (or it's meant
   to be permanent or unattended), run `tunlite install service` to register OS
   autostart. A tunnel the user assumes is "always on" that silently vanishes after
   a reboot is the most common surprise — don't skip the question; but don't enable
   it without telling them either (it writes a real launchd/systemd/Task Scheduler
   service).

Newer than 0.10.x? Proceed anyway — don't stop because the tool is ahead. Only if a step
below returns an unexpected usage (exit 2) or not-found (exit 3) error is the skill likely
stale; refresh it with `tunlite install skill` and re-read before retrying.

## Exit codes (stable — branch on these)

| code | meaning | what to do |
|---|---|---|
| 0 | ok | continue |
| 2 | usage error | fix the command syntax |
| 3 | not found | the named tunnel/host doesn't exist |
| 4 | needs-auth | target is not passwordless — see "Passwordless" below |
| 5 | daemon unreachable | run `tunlite daemon start` (or `tunlite enable` auto-starts it) |
| 1 | other error | read the JSON `error` field |

## Core workflow

```bash
# 1. Define a tunnel — forwards use ssh-native repeatable flags -L / -R / -D.
#    Spec is [bind:]PORT:HOST:HOSTPORT (-L/-R) or [bind:]PORT (-D). The name is a
#    bare positional; the target rides in --to; at least one forward is required.
tunlite add web-8080 --to user@host -L 8080:localhost:80 --json   # reach server's :80 at localhost:8080
tunlite add rev-9000 --to user@host -R 9000:localhost:3000 --json # expose local 3000 on server:9000
tunlite add px-1080  --to user@host -D 1080 --json                # local SOCKS5 proxy on :1080
tunlite add multi    --to user@host -L 8080:localhost:80 -D 1080 --json  # several forwards on one tunnel
#   SSH port rides in the target: --to user@host:2222
#   -L = reach a remote service locally; -R = expose a local service on the server; -D = local SOCKS5.
#   a bind: prefix is the listen address (omit = localhost; 0.0.0.0 to expose on all interfaces);
#   IPv6 literals must be bracketed, e.g. -L [::1]:8080:localhost:80
#   options: -i <keyfile>  --jump [user@]bastion[:port]  --ssh-opt "-o Foo=bar"  --tag <label>  --disabled  --no-auto-key
#   change an existing tunnel in place (set is the SOLE forward editor):
#     tunlite set <name> --to user@newhost --json          # edit connection only; forwards untouched
#     tunlite set <name> -L 8080:localhost:80 -D 1080 --json   # REPLACE the whole forward set (destructive)
#   label tunnels and act on a group:       tunlite add ... --tag prod ;  tunlite enable --tag prod --json

# 2. Turn it on (enables it, probes passwordless, auto-starts the daemon)
tunlite enable web-8080 --json
#   then ASK the user: should this survive reboot/login? if yes -> tunlite install service
#   (enable alone does NOT restart the tunnel after a reboot; see "Autostart")

# 3. Enumerate definitions / check structured runtime state
tunlite list --json          # defined tunnels (from config)
tunlite status --json        # {daemon:{running,pid,version,uptimeMs,uptime}, tunnels:[{name,host,state,pid,uptimeMs,uptime,restarts,lastError,forwards,tags}]}
tunlite status --tag prod --json   # filter the table to a tag (or `tunlite list --tag prod`)
tunlite status web --json

# 3b. Diagnose connection problems (ssh/keys/ports/daemon/service)
tunlite doctor --json        # {ok, summary:{ok,warn,fail}, checks:[{group,id,title,status,detail,fix}]}
tunlite doctor web-8080 --json   # focus on one tunnel

# 4. Rename / restart / take down / remove
tunlite rename web web-prod-8080 --json   # rename (cleanly hands the live tunnel over)
tunlite restart web --json                # bounce a running tunnel (no intent change)
tunlite disable web --json                # stop + disable (won't come back on reboot)
tunlite rm web --json                     # delete the definition entirely
```

`state` values: `idle`, `starting`, `connected`, `retrying`, `needs-auth`,
`blocked`, `failed`, `stopped`, `disabled`, `daemon-stopped`. Treat `connected` as success.
`retrying` is normal during a transient outage (the daemon backs off and reconnects).
`blocked` = another tunnel already holds one of this tunnel's forward endpoints; it retries until that endpoint frees up.

For a live, interactive view use `tunlite monitor` (top-style; start/stop/restart by key). `status --json` remains the scriptable one-shot.

## Naming convention (use this when you create or rename tunnels)

Name tunnels **`<purpose>-<port>`, all lowercase, words hyphenated, no spaces**,
where `<port>` is the tunnel's primary port. The name should say *what it is* and
*which port*, so `status`/`logs` read self-explanatory.

- ✅ `tmux-prod-19999` · `progress-board-4705` · `db-staging-5432` · `socks-1080`
- ❌ `Tunnel1` · `MyTmux` · `progress board 4705` (spaces/caps) · `web` (no port)

Allowed characters: letters, digits, `.` `_` `-` (no spaces). To fix an existing
name, use `tunlite rename <old> <new>` rather than rm + add — it preserves the
tunnel's config and hands the live connection over cleanly.

## Passwordless

The daemon runs `ssh` non-interactively (`BatchMode=yes`), so a target that
isn't passwordless shows up as `state: "needs-auth"` and `tunlite status` exits 4.

- First check: `tunlite check user@host --json` → `{passwordless: true|false}`,
  exit 0 if already set up, 4 if not.
- To establish it: `tunlite setup-key user@host` installs your public key on the
  target. **This needs the target password typed once at an interactive
  terminal.** As an agent you usually cannot type that password, so:
  - If a human is available, ask them to run `tunlite setup-key user@host`.
  - Otherwise confirm the host is already passwordless before adding the tunnel.
- `tunlite enable` will *attempt* setup automatically only when run in an interactive
  TTY with `autoSetupKey` on (the default). In non-interactive/agent contexts it
  will not prompt — it starts the tunnel and reports `needs-auth`.

## Autostart (register the daemon to OS startup)

**Ask the user about this proactively** whenever a tunnel should stay up — don't
wait for them to bring it up. A tunnel survives reboot/login only if the daemon is
registered to OS startup; `enable` alone does not do that. Confirm first (it writes
a real launchd/systemd/Task Scheduler service), then:

```bash
tunlite install service --json     # launchd (macOS) / systemd --user (Linux) / Task Scheduler (Windows, beta)
tunlite status --json              # service state folded in under {service: {installed, running, ...}}
tunlite uninstall service --json   # unload the service AND remove the file it created
tunlite uninstall --json           # full teardown: stop daemon + remove service + skill + launcher/lib
tunlite uninstall --purge --json   # also delete config + logs
```

A bare interactive `tunlite uninstall` asks to confirm (and warns if tunnels are
up). `--json` already skips that prompt; `--force` skips it without `--json`.

The OS keeps the **daemon** alive; the daemon keeps the **tunnels** alive and
reconciles from the config on every (re)start, so enabled tunnels resume after
reboot/login.

Shell tab-completion (human convenience, not needed by agents): enable with
`tunlite install completion` (auto-detects the shell; or pass `bash|zsh|fish`),
remove with `tunlite uninstall completion`.

## Logs & daemon control

```bash
tunlite logs web -n 50        # last 50 lines for a tunnel
tunlite logs web -f           # follow (streams; stop by ending the process)
tunlite daemon status --json  # {running, pid, version}
tunlite daemon start|stop     # advanced/plumbing — you normally use up/down instead
```

The daemon starts only via `tunlite enable` (on demand) or `tunlite install service` (at
login). Other commands won't start it; if it's down they exit `5` and tell you
how. Note: with the service installed, `tunlite daemon stop` is temporary (the OS
restarts it) — use `tunlite uninstall service` to stop for good.

## Foreground one-shot: `run` (daemon-less, machine-readable)

`run` brings up a **single** tunnel in the foreground, supervised but with **no
daemon and no config** — it is built entirely from inline flags. This is the
agent-friendly way to run one tunnel as a container / systemd entrypoint and watch
its state: it never reads or writes the config file.

```bash
tunlite run --to user@host -L 8080:localhost:80 --json            # NDJSON state on stdout
tunlite run --to user@host -D 1080 --name socks --exit-on-failure --json
#   forwards: same -L / -R / -D as add/set (repeatable; at least one required)
#   --name LABEL   self-reported name in the state output (defaults to the target host)
#   --json         emit NDJSON state objects on stdout (one per line); else human lines on stderr
#   --exit-on-failure   exit non-zero on a failure state instead of retrying forever
#   common: -i <keyfile>  --jump [user@]bastion[:port]  --ssh-opt "-o Foo=bar"
```

- **State reporting:** with `--json`, each state transition is one NDJSON object on
  **stdout**: `{ts, name, state, pid, restarts, uptimeMs, lastError, lastExitCode}`
  — `ts` is epoch milliseconds; the other fields are the tunnel's changing state,
  with the same names and `state` values as `tunlite status --json` (so one parser
  fits both). Without `--json`, human-readable status lines go to **stderr**.
- **Signals:** `SIGTERM`/`SIGINT` trigger a clean shutdown → exit `0`.
- **Exit codes** (only enforced with `--exit-on-failure`; otherwise the supervisor
  keeps retrying and only stops on a signal):
  - `0` clean shutdown (signal) or normal stop
  - `4` `needs-auth` (target not passwordless)
  - `1` `blocked` / `failed` (e.g. listen port in use), or `ssh` binary not found
  - `2` usage error (missing `--to`, no forward, or a bad target/jump/forward spec)
- To keep one tunnel alive on this machine across reboots, prefer `add` + `up` +
  `install service` (daemon-managed). Use `run` when something else (a container
  runtime, systemd unit, CI job) owns the process lifecycle and you want the
  tunnel's state streamed to that supervisor.

## Disconnect alerts (webhook)

The daemon can POST a JSON event to a webhook on tunnel and daemon lifecycle edges.

```bash
tunlite webhook --json                                        # {url, channel, enabled, events}
tunlite webhook set https://hook/x --json                     # set + enable (channel auto-detected from URL)
tunlite webhook set https://hook/x --events tunnel,daemon-crash --json
tunlite webhook events down,recovered --json                  # change subscribed events
tunlite webhook off --json                                    # disable (use `webhook on` to re-enable)
tunlite webhook test --json                                   # POST a test event -> {ok, status}
```

Events are edge-triggered (a reconnect storm alerts once). Two scopes:
- tunnel: `up`, `down`, `recovered`, `needs-auth`, `failed`, `stopped`
- daemon: `daemon-up`, `daemon-down`, `daemon-crash`

`webhook events` accepts names, the groups `tunnel`/`daemon`, `all`, or `none`.
Default subscription: `down, recovered, needs-auth, failed, daemon-crash`.
Payload: `{scope, tunnel, host, event, state, lastError, restarts, ts, machine, version}`.

## Config import / export

```bash
tunlite export --json > backup.json     # dump portable tunnels only; no machine-local settings
tunlite import backup.json --json       # merge tunnels: {added, skipped, overwritten}
tunlite import backup.json --force --json   # overwrite same-name tunnels
```

`import` merges **tunnels only** (same-name skipped unless `--force`); it never
changes local settings/alerts. A missing file exits 3; a malformed file exits 1
and leaves the current config untouched.

## Tips / gotchas

- `enable`/`disable` are persistent (they flip the tunnel's `enabled` flag and survive
  reboot). Use `restart` to bounce a running tunnel without changing intent.
- The action verbs `enable`/`disable`/`restart` must name a target — a tunnel name,
  `--tag <label>`, or the literal `all` (e.g. `tunlite enable all`). Bare (no target)
  is a usage error (exit 2), not a silent "all" — `all` is a reserved tunnel name. The
  read-only views `status`/`list`/`monitor` still default to all when bare.
- A mistyped command suggests the nearest one (`tunlite stauts` → "did you mean
  `status`?"); the old `up`/`down` point at `enable`/`disable`.
- Tags group tunnels: `--tag <label>` (repeatable) on `add`/`set` labels a tunnel
  (`set --no-tags` clears); `enable`/`disable`/`restart`/`status`/`list`/`monitor` accept
  `--tag <label>` to act on every tunnel carrying it (multiple `--tag` = union). A
  name and `--tag` are mutually exclusive (exit 2); for the one-shot commands
  (`enable`/`disable`/`restart`/`status`/`list`) a tag that matches nothing exits 3
  (`monitor` is a live view, so it just shows an empty dashboard instead).
  Tags are metadata only — they never change the ssh command. Each tunnel's
  `tags` array is in `list --json` / `status --json`.
- Forwards use ssh-native flags on `add`/`set`/`run` (repeatable — one tunnel can
  carry several): `-L [bind:]PORT:HOST:HOSTPORT` (reach a remote service locally:
  you connect to `localhost:PORT`, it tunnels to `HOST:HOSTPORT` on the server
  side), `-R [bind:]PORT:HOST:HOSTPORT` (expose a local service on the server: the
  server listens on `PORT` and tunnels back to `HOST:HOSTPORT` reachable from your
  machine), `-D [bind:]PORT` (local SOCKS5 proxy). A `bind:` prefix is the listen
  address — omit it for `localhost`, write `0.0.0.0` to expose on all interfaces;
  IPv6 literals must be bracketed (`[::1]`). `set <name> -L/-R/-D …` is the only
  way to change a tunnel's forwards: passing any forward flag **replaces the whole
  set** (destructive; the new set is echoed back), while `set` with no forward flag
  leaves forwards untouched.
- A `failed` state with an "address already in use" `lastError` means the listen
  port is taken — pick another port; the daemon won't hot-loop on it.
- Relocate all state for testing/sandboxing with env vars: `TUNLITE_HOME`,
  `TUNLITE_CONFIG`, `TUNLITE_SOCKET`, `TUNLITE_SSH` (override the ssh binary).
