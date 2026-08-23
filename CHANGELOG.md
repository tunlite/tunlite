# Changelog

All notable changes to **tunlite** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and versions follow
[Semantic Versioning](https://semver.org/) (`MAJOR.MINOR.PATCH`, tags `vX.Y.Z`,
monotonically increasing). See [docs/VERSIONING.md](docs/VERSIONING.md) for the
release process.

> tunlite debuted publicly at **0.9.0**. Earlier `0.x` releases were a private
> prototype and are not part of this public history.

## [1.0.0-preview.1] - Unreleased

This preview replaces the Node.js runtime with a single Go binary while keeping SSH
as the product's focused entry point.

### Added

- Generic Connection, Driver, and deterministic ExecutionPlan boundaries with a
  built-in OpenSSH driver for `-L`, `-R`, `-D`, multiple forwards, jump hosts,
  identity files, and strict `Key=Value` SSH options.
- Per-Connection systemd user services and launchd jobs with explicit foreground,
  temporary background, and cross-login modes; `add` materializes a stopped native
  service, while `start`/`stop` and `enable`/`disable` select how it runs.
- Zero-interruption `manage`/`unmanage` for compatible native SSH services, plus
  explicit two-way drift resolution through `resolve --use tunlite|system`.
- Integer-versioned CLI response envelopes, versioned portable configuration,
  embedded schemas, dry-run and non-interactive guarantees, structured actions and
  risk warnings, batch partial-failure semantics, and five independent state dimensions.
- Reproducible macOS and Linux release archives and `SHA256SUMS`; macOS ships as one
  Universal 2 binary for both Intel and Apple Silicon, while Linux keeps separate
  x64 and arm64 archives. Go CI also keeps the future Windows core build compiling
  without presenting it as a supported v1 preview artifact.
- Color-aware aligned status output, per-Connection status details, an environment
  and Connection `doctor`, and the foreground `tunlite monitor` dashboard. Human
  output respects TTY detection and `NO_COLOR`; JSON never contains ANSI escapes.
- Chinese and English human interfaces selected by `--lang`, `TUNLITE_LANG`, or the
  system locale. Command names, configuration, error codes, and JSON fields and
  values remain stable English machine contracts.
- One canonical configuration path, root-only `configVersion`, one optional Group
  per Connection for grouped status and batch selection, and explicit `--all`.
- A single native binary whose installation, upgrades, and removal are owned by the
  release channel or package manager rather than the core CLI.
- Shell completion is deliberately deferred from the v1 preview until ownership
  across shells, platforms, and package managers has a durable design; the CLI does
  not read or write shell startup files.
- A standalone `use-tunlite` Agent Skill package whose CLI contract is checked in CI;
  installation, updates, and removal remain owned by the Agent.

### Changed

- Persistent Connections are owned directly by the operating system and execute the
  Provider command without a central Tunlite controller or IPC socket.
- `tunlite monitor` now uses a full-screen, `top`-style dashboard with the running
  version, state totals, terminal-width-aware columns, detail navigation through
  Enter/right and Esc/left, visible `starting`/`stopping`/`restarting` transitions,
  and `y` confirmation before stop or restart. Esc exits from the outer view.
- Human status views now explain lifecycle through `STATE`, `RUNS AS`, and
  `ON LOGIN` instead of exposing the internal `MODE` directly. Monitor keeps PID,
  uptime, and restart count as operational columns, drops the redundant `TYPE`
  column, and hides host/route details first on narrow terminals.
- V1 exposes one optional `group`. `list`, `status`, and `monitor` group human output
  by this stable business ownership; `--group` selects one group for batch operations.
- Interactive commands that may wait for SSH, native service managers, or batch
  observation now print the operation before waiting; JSON output remains free of
  human progress text, and `version --json` retains its protocol version field.
- The installer downloads and verifies a platform Go binary; Node.js and npm are no
  longer v1 runtime prerequisites.
- The Agent Skill now follows the runtime capability and schema contract and is tested
  against commands, exit codes, and state dimensions.

### Deferred

- Webhook connection alerts are intentionally deferred while v1 restores the core
  CLI experience. The preview does not advertise or silently accept inactive webhook
  configuration.

### Removed

- The central supervisor, IPC protocol, PID ownership, daemon lifecycle alerts,
  implicit lifecycle-triggered SSH key installation, and the second layer of global lifecycle
  management from the v1 runtime.
- The archived Node runtime, npm package, Node test suite, 0.x decoder, migration
  fixtures, and migration E2E from the active V1 branch. The immutable `v0.10.3` Tag
  and private archive retain the historical implementation.
- CLI-managed Agent Skill installation; Tunlite no longer inspects Agent-specific
  extension directories.
- The preview-only `--config`, approval state, `setup-key`, `migrate`, self-install,
  self-update, self-uninstall, `tun` alias, hidden Tag aliases, and unpublished
  configuration/service-record compatibility layers.

## [0.10.3] - 2026-08-16

This is the final planned release of the Node.js implementation. The Node line is
now frozen except for critical security, data-loss, or Go-migration fixes.

### Fixed
- **Reinstalling could strand the daemon and lock you out** when the autostart
  service was registered and you were reinstalling *over a tunlite tunnel* (e.g.
  `npx tunlite@latest install -y` to recover a broken `update`). The macOS launchd
  adapter refreshed an already-loaded service with `unload` then `load` — two
  non-atomic steps. The `unload` stopped the daemon, dropping every tunnel; if the
  reinstall rode one of those tunnels, its own session was severed and the process
  was killed before `load` ran → the service was left unloaded with nothing to
  relaunch it. Reinstall now restarts an already-loaded, unchanged job with
  `launchctl kickstart -k` (launchd owns the relaunch, so a severed session can't
  strand it). If the pinned Node, entry, or log path changed, a detached one-shot
  helper safely reloads the launchd definition, retries bootstrap, and restores the
  previous plist if the new definition cannot load. The systemd adapter now applies
  new code via an init-owned `systemctl --user restart` (the previous `enable
  --now` was a no-op on an already-running unit, so the freshly anchored code never
  loaded). Updates also converge the persisted service definition before the
  restart, so changing the pinned Node path cannot leave launchd or systemd trying
  to execute a stale binary. The detached launchd helper waits through launchd's
  restart-throttle window, including an update immediately after installation.
- **`tunlite update` could strand the daemon the same way** when no autostart
  service was installed: it shut the daemon down and respawned it from the
  foreground, so a restart that dropped the tunnel carrying your own session killed
  the command before the respawn → daemon stranded. `update` and reinstall now share
  one restart strategy: with a service, the OS service manager restarts the daemon
  (`launchctl kickstart -k` / `systemctl --user restart`); without one, the daemon
  relaunches **itself** — it spawns a fresh, detached successor as it exits, so the
  relaunch is owned by the daemon, never by a caller whose session the restart may
  sever.
- **A leftover launchd plist was mistaken for a loaded service.** An update then
  attempted `kickstart`, which could not work, instead of using the safe
  serviceless self-relaunch path. Status now distinguishes installed files from
  loaded Jobs.

### Docs
- Corrected the bundled Agent Skill: `export` contains the portable tunnels only,
  not machine-local settings.

## [0.10.2] - 2026-06-23

### Changed
- `status` and `monitor` now render a **remote (`-R`) forward's route** with the local
  side on the left and the arrow following the traffic — `localhost:3000 ← host:9000`,
  the same way local forwards already read (local on the left, arrow pointing at the
  far end). Just a clearer, more consistent route column at a glance; tunnels and
  config are untouched.

## [0.10.1] - 2026-06-20

### Added
- `tunlite doctor` now reports **agent-skill freshness** — a `skill-fresh` check
  that warns when an installed copy of the bundled `ssh-tunnel` skill has drifted
  behind the one this version ships (the usual cause: `tunlite update` bumped the
  tool but the skill copy stayed behind). It compares content, not a version stamp,
  so a release that didn't touch the skill flags nothing. Refresh with
  `tunlite install skill`.

### Changed
- The bundled **`ssh-tunnel` agent skill** now tells the agent to **proactively ask
  whether a tunnel should start at login/boot** and, on a yes, run
  `tunlite install service`. Neither a bare `install` nor `enable` registers OS
  autostart, so a tunnel meant to be "always on" no longer silently vanishes after a
  reboot. The skill's install bootstrap (Step 0) was also rewritten around a single
  anchored install — every entry point (npm, curl, bundled copy) just seeds it.

### Docs
- README and the doc site now lead with tunlite's agent-first story ("for you and
  your Agent"), and a new plain-text guide at `tunlite.dev/skill.txt` lets an AI
  Agent install tunlite and register its skill by reading one link.

## [0.10.0] - 2026-06-19

### Changed (BREAKING)
- Renamed the tunnel-control verbs `up`/`down` → `enable`/`disable`. They always
  wrote the persistent `enabled` flag (a disabled tunnel stays down across daemon
  restarts and reboots), so the new names match the rest of the vocabulary
  (`--disabled` on `add`, the `disabled` state) and keep that persistent "intent"
  axis cleanly separate from the runtime axis (`daemon start`/`stop`, which run or
  stop the supervisor process now). The webhook `up`/`down` events and the monitor
  arrow-key navigation are unchanged. Migration: `tunlite up [name]` →
  `tunlite enable [name]`; `tunlite down [name]` → `tunlite disable [name]`.
- The action verbs `enable`/`disable`/`restart` now require an explicit target — a
  tunnel name, `--tag <label>`, or the literal `all` — instead of treating a bare verb
  as "all". Bare `tunlite enable` (which read like "enable the tool" and silently
  flipped every tunnel) is now a usage error (exit 2) pointing you to a target. `all`
  is a reserved tunnel name. The read-only views `status`/`list`/`monitor` still
  default to all. Migration: `tunlite enable` (no args) → `tunlite enable all`.
- Forwards are now defined with ssh-native flags `-L`/`-R`/`-D` (repeatable),
  shared by `add`, `set`, and `run`. `add` reshaped:
  `tunlite add <name> --to user@host -L 8080:localhost:80 -D 1080`.
  Migration: `add local <name> --remote 80 --local 8080` → `add <name> -L 8080:localhost:80`;
  `add remote <name> --local 3000 --remote 9000` → `add <name> -R 9000:localhost:3000`;
  `add dynamic <name> --local 1080` → `add <name> -D 1080`.
- `install` is now a single guided entry point. Bare `tunlite install` anchors the
  runtime and (in a terminal) asks whether to add autostart / shell completion /
  the agent skill; `tunlite install -y` says yes to all; with no `-y` and no
  terminal it only anchors. The per-step opt-in/opt-out flags
  (`--service`/`--skill <dir>`/`--completion` and `--no-*`) and their env twins
  (`TUNLITE_SERVICE`/`TUNLITE_SKILL`/`TUNLITE_COMPLETION`) are gone — to set up one
  piece on its own, use the positional `install service|skill|completion`. Passing a
  removed flag now exits 2 with guidance.
- `export` now prints only your tunnels (`{ version, tunnels }`) — the exact shape
  `import` reads — instead of the whole config. It previously also dumped the
  `settings` block, but `import` only ever merges tunnels and never reads settings,
  so the settings were redundant; worse, the webhook url in them was redacted to a
  display form (`https://host/…`), so an `export`→`import` round-trip looked lossless
  while silently carrying a corrupted, unusable url. Settings (backoff/keepalive
  tuning, the webhook url + token) are machine-local and stay out of the portable
  export. To see them, read `config.json` (path shown by `tunlite doctor`).

### Added
- `tunlite run` — a daemon-less, foreground, supervised tunnel for container /
  systemd entrypoints (`--json` NDJSON state, `--exit-on-failure`).
- `set <name> -L/-R/-D …` replaces a tunnel's whole forward set.
- "Did you mean": a mistyped command or subcommand now suggests the nearest one
  (`tunlite stauts` → "did you mean `status`?"), and the retired `up`/`down` (plus
  `start`/`stop`) point at `enable`/`disable`. Covers the top-level commands and the
  `daemon`/`webhook`/`install`/`uninstall` subverbs.
- `all` as an explicit target for `enable`/`disable`/`restart` (e.g.
  `tunlite enable all`) — every tunnel, stated rather than implied.
- The route column (and the `add`/`set` echo) now points its arrow at the far
  side: a local forward reads `:8080 → :80` and a remote forward `:9000 ← :3000`.
  Endpoints keep ssh-flag order; the arrow direction alone distinguishes local
  from remote at a glance.

### Removed (BREAKING)
- The `forward list|add|rm` command group. Use `set <name> -L/-R/-D …` to redefine
  a tunnel's forwards.

### Fixed
- `install --skill` no longer fails right after a global npm install. The skill
  step resolved its source relative to the running copy, but `install` runs it
  after `anchor()`'s legacy cleanup removes that copy (the npm-global dir), so it
  reported `skill source not found` and skipped the agent skill. It now falls
  back to the freshly anchored `libDir` copy recorded in the install manifest.
- `--json` now applies to error paths too. Usage and not-found errors previously
  printed plain text to stderr even under `--json`; they now emit
  `{ "error": …, "code": N }` on stdout (exit code unchanged). Covers `add`,
  `set`, `rm`, `rename`, `status`, `run`, `webhook`, `import`, unknown commands,
  and more. Human (non-`--json`) output is unchanged.
- `status --json` tunnel objects now carry `lastExitCode` in every state. It was
  present only for live tunnels (from the supervisor) and missing for
  idle/stopped/`daemon-stopped` ones, so an agent's parsed schema shifted with
  tunnel state; the key is now always present.
- Doc site: the `blocked` state (endpoint conflict) is now listed in the
  documented state values, matching the CLI and `SKILL.md`.

## [0.9.5] - 2026-06-18

### Fixed
- **`tunlite update` no longer fails with a bogus `could not fetch … (need curl
  or wget)` once the repository archive grows past ~1 MB.** The fetcher captured
  the tarball into `spawnSync`'s 1 MB-default stdout buffer, so a larger archive
  overflowed it (`ENOBUFS`, the fetcher killed with `SIGTERM`) and surfaced as a
  missing-tool error even though curl/wget were installed and the download was
  reachable. It now downloads to a file (no in-memory buffer, any size) and the
  error message distinguishes a genuinely absent tool from a failed download,
  including the fetcher's own stderr.
  - Already on 0.9.4 or earlier? That broken `update` can't pull this fix itself —
    re-install once with `npx tunlite@latest install`; subsequent `tunlite update`
    runs then work normally.

## [0.9.4] - 2026-06-18

### Fixed
- **`logs <name>` now exits not-found (`3`) for an unknown tunnel** instead of
  silently tailing an empty channel and exiting `0` — it was the only
  name-taking command that didn't honor the not-found contract, so an agent
  probing `tunlite logs typo --json` read it as "succeeded, no logs". A bare
  `logs` with no name still tails the daemon's own log.
- **`add` refuses a duplicate name (exit `2`) instead of silently overwriting.**
  It used to upsert, so re-adding an existing name replaced the prior definition
  and exited `0` — quiet data loss. Use `set <name> …` to change a tunnel in place
  or `rm <name>` first. An invalid tunnel name is now a usage error (`2`) too,
  rather than bubbling up as a generic error (`1`).
- **`list` / `status` with a `--tag` that matches nothing now exit not-found (`3`)**,
  matching `up`/`down`/`restart` and the documented tag contract. `list` returned
  `0` and `status` returned `0` (daemon up) or even `5` (daemon down); the tag is
  now checked against config up front, so the answer is a consistent `3` either way.
  (`monitor` is a live dashboard, so it shows an empty view rather than exiting.)
- **Anchored-install detection is now symlink-tolerant.** `isAnchored()` compared
  the manifest's literal `libDir` against the realpath-resolved running directory,
  so when the install dir was reached through a symlink — `/home` → `/var/home` on
  Fedora Silverblue/CoreOS/openSUSE MicroOS, or `/tmp` → `/private/tmp` on macOS —
  a correct install reported un-anchored forever: the "run `tunlite install`" nudge
  never cleared and `tunlite update` refused to self-update. It now compares
  canonical paths on both sides.
- **`status <name> --json` now exits not-found (`3`) for an unknown tunnel**,
  matching the human path. It returned the full snapshot with an empty
  `tunnels[]` and exit `0`, so an agent couldn't tell "doesn't exist" from
  "exists" by exit code; it now emits `{error, code: 3}` and exits `3`.
- **`<command> --help` / `-h` now prints help and exits `0` for every command**,
  as the README promises. Previously only the bare command position honored it;
  placed after a verb (`tunlite status --help`) it fell through and was rejected
  as an unknown option — and `tunlite export --help` ignored the flag and dumped
  the config. `-h`/`--help` are not real flags on any command, so they are now
  intercepted globally.

## [0.9.3] - 2026-06-17

### Added
- **`uninstall` now confirms before tearing everything down**, and warns when
  tunnels are currently up (counted from the daemon), so you can't wipe a live
  setup by reflex. `--force` skips the prompt; `--json` and non-interactive runs
  proceed unprompted as before.

### Changed
- **`tunlite update` upgrades to the latest published *release tag*, not the
  branch tip.** It resolves the newest `vX.Y.Z` tag and re-anchors that, so a
  self-update can only ever land on a real released version (which, by release
  discipline, is also on npm) — the installed version can no longer drift onto an
  unreleased commit, and `npm`'s view can't diverge from what's actually running.
- **`update` refuses a non-anchored install with the right per-channel guidance:**
  a git checkout → `git pull`; an `npm i -g` install → `npm i -g tunlite@latest`
  (so that channel's version metadata stays authoritative); anything else → re-run
  `npx tunlite install`.

### Docs
- Slimmed the README (EN/ZH) to a concise landing that points to the documentation
  site for the deep reference; trimmed this changelog to the public era (0.9.0+);
  de-duplicated the quickstart — `install service` is optional autostart that also
  brings tunnels up, so it no longer reads as a step after `up`.

## [0.9.2] - 2026-06-17

### Fixed
- **Too-old Node now fails fast with a clear message instead of a cryptic
  crash.** Running under Node < 18 (e.g. an old `nvm` default) let `install`
  print success and then throw a `SyntaxError` from a module using newer syntax,
  with no hint why. The entry point now checks the Node version first — before
  requiring any modern module — and exits with `tunlite requires Node.js >= 18 …`
  plus the `nvm install 18` fix. The guard lives in a small ES5-only
  `src/node-check.js` so it parses on any runtime.
- **`curl … | sh` now prompts instead of silently skipping setup.** Piped into a
  shell the installer's stdin carries the script, so the interactive service /
  skill / completion questions were skipped — nothing got registered and the
  daemon never started, making it look like `install` hadn't run. `confirm()`
  now reads the answer from the controlling terminal (`/dev/tty`) when stdin is
  piped, so the one-liner asks the same y/N questions as a direct run (pre-answer
  with `--service` / `--no-skill` / … to stay non-interactive). Both bootstraps
  also verify Node ≥ 18 up front. On Windows (no `/dev/tty`) the bootstrap
  registers autostart by default; the skill and completion stay opt-in.

## [0.9.1] - 2026-06-16

### Fixed
- **`install` confirmation prompts were invisible on a real terminal.** The
  prompt was written separately and then readline opened with an empty query;
  in terminal mode readline's line refresh erased the just-written text, leaving
  a blank line where the question should be (you had to answer blind). The
  prompt is now rendered by readline itself, so it always shows.

## [0.9.0] - 2026-06-16

First public release.

### Added
- **ProxyJump / bastion support.** `--jump [user@]host[:port][,...]` on `add`,
  `check`, and `setup-key` (and a per-tunnel `jump` field) makes tunnels through a
  jump host first-class, instead of needing raw `--ssh-opt`.
- **Multiple forwards per tunnel.** `tunlite forward list|add|rm <tunnel>` manages
  several `-L`/`-R`/`-D` forwards carried over a single ssh connection.
- **Edit existing tunnels.** `tunlite set <name> [--to …] [-i key] [--jump …]
  [--ssh-opt …] [--auto-key|--no-auto-key]` changes a tunnel's connection settings
  in place (forwards stay managed by `forward`).
- **Tags / groups.** Label tunnels with `--tag <label>` (repeatable) on `add`, edit
  with `set --tag` / `set --no-tags`, then act on a whole group: `up`/`down`/
  `restart`/`status`/`list`/`monitor` accept `--tag <label>` to select every tunnel
  carrying it (multiple `--tag` = union). A name and `--tag` are mutually exclusive.
  Tags are metadata only and never change the ssh command; the `tags` array shows up
  in `list --json` / `status --json`.

### Fixed
- The passwordless probe behind `check` / `up` / `doctor` now applies the tunnel's
  jump hosts and ssh options, so a jump-only host is no longer misreported as not
  passwordless.
- **Reconnect backoff** now resets only after a connection has been sustained for
  `resetAfterMs` (60s), not the instant it reaches "connected". A tunnel that keeps
  dropping right after connecting backs off as intended instead of reconnecting at
  the base delay indefinitely.
- **No more double daemon.** A racing second `up` no longer unlinks a live
  daemon's control socket; a stale socket is removed only after probing that no
  daemon answers. `stop()` also reliably escalates to SIGKILL when an ssh child
  ignores SIGTERM, instead of leaving it wedged.
- **`logs` no longer truncates** — a non-follow `logs -n <N>` waits for the daemon's
  real end-of-stream instead of a fixed 200 ms window, so a large `-N` returns every
  line. An interrupted `install` also self-heals its runtime swap (restores the
  previous copy) rather than leaving no runtime behind.

### Security
- **ssh argument injection closed.** A host or jump hop beginning with `-` is
  rejected on config load, and a `--` terminator precedes the destination, so a
  crafted target can no longer be read by ssh as an option (e.g. `-oProxyCommand=…`).
- **Daemon control socket locked to the owner** — its directory is created `0700`
  and the socket `0600` on Unix, so other local users can't drive the daemon on the
  `~/.tunlite` fallback (used when `XDG_RUNTIME_DIR` is unset, e.g. on macOS).
- **Webhook URLs are redacted** in all CLI output, `--json`, daemon logs, and
  `export` (the Slack/WeCom token rides in the URL); the daemon refuses a
  non-`http(s)` webhook URL.
- **Self-update fetches over `https` or `file` only** — an `http` downgrade or a
  hostile mirror can no longer feed arbitrary code into the updater.
- **Webhook responses are capped at 8 KB** so a hostile or misbehaving endpoint
  can't stream unbounded data into the daemon process.
- **`setup-key` validates the public key** — it refuses a key containing a newline
  or a single quote before building the remote command, closing a shell-injection
  path through a crafted key comment.

[0.10.3]: https://github.com/yuanyuanzijin/tunlite/compare/v0.10.2...v0.10.3
[0.10.2]: https://github.com/yuanyuanzijin/tunlite/compare/v0.10.1...v0.10.2
[0.10.1]: https://github.com/yuanyuanzijin/tunlite/compare/v0.10.0...v0.10.1
[0.10.0]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.5...v0.10.0
[0.9.5]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.4...v0.9.5
[0.9.4]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.3...v0.9.4
[0.9.3]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.2...v0.9.3
[0.9.2]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/yuanyuanzijin/tunlite/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/yuanyuanzijin/tunlite/releases/tag/v0.9.0
