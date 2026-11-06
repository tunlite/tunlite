# Tunlite

> Reliable SSH connections for developers and AI agents.

**English** · [简体中文](README.zh-CN.md)

[![Go CI](https://github.com/tunlite/tunlite/actions/workflows/go-ci.yml/badge.svg)](https://github.com/tunlite/tunlite/actions/workflows/go-ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Tunlite turns OpenSSH port forwards into named connections that can run in the
foreground or as isolated operating-system services. It uses the `ssh` already on
your machine, creates no account or server, and does not invent a transport protocol.

The v1 preview is a single Go binary. There is no central background controller:
each persistent connection is owned directly by systemd or launchd, so one failing
connection does not take the others down.

## Install the preview

The system `ssh` command is the only runtime prerequisite. On macOS, install the
published preview with [`tunlite/tap`](https://github.com/tunlite/homebrew-tap).
Homebrew downloads the checksum-verified release binary; it does not install Go
or compile source:

```bash
brew install tunlite/tap/tunlite
```

If you already tapped an earlier package, run `brew update` before installing or
upgrading. `brew upgrade tunlite/tap/tunlite` upgrades the old source-built package
to the binary package with the matching Skill at revision 2; use `brew reinstall tunlite/tap/tunlite` to
reinstall the current package. Homebrew delivery does not confer Apple developer
signing/notarization or guarantee acceptance by enterprise policies.

For optional manual source compilation, install Go 1.25 or newer and run:

```bash
git clone --depth 1 --branch v1.0.0-preview.1 https://github.com/tunlite/tunlite.git tunlite-source
cd tunlite-source
go install -trimpath -ldflags='-s -w -X github.com/tunlite/tunlite/internal/cli.Version=1.0.0-preview.1' ./cmd/tunlite
```

Use this tagged checkout: remote `go install ...@v1.0.0-preview.1` currently fails
on a Go checksum lookup for the repository's older, pre-Go `v0.10.3` tag.
The executable goes to `GOBIN`, or the first `GOPATH` entry's `bin` directory
(normally `~/go/bin`). Add that directory to `PATH`. Source builds normally avoid
browser-download quarantine, but do not provide Apple developer signing or
notarization and may still be restricted by enterprise policies.

Prebuilt downloads remain available. Once the corresponding GitHub Release and
checksum manifest are available:

```bash
# macOS / Linux; downloads v1.0.0-preview.1 and verifies SHA-256
curl -fsSL https://raw.githubusercontent.com/tunlite/tunlite/main/bootstrap.sh | sh
```

Every release contains self-contained archives and `SHA256SUMS`. Follow the
[zero-to-acceptance guide](docs/INSTALL_AND_ACCEPTANCE.md) for installation and
platform verification. See the [preview guide](docs/V1_PREVIEW.md) for current
version boundaries and release gates.

Newly built archives also contain `skills/use-tunlite/SKILL.md`, copied from the
same source checkout as the binary and covered by the archive checksum and
attestation. The existing public `v1.0.0-preview.1` archives remain unchanged and
do not contain it. For that release, Homebrew installs the matching Skill from a
checksum-pinned resource at the same release tag. Bootstrap installs the binary
and prints the same-version upstream Skill URL without downloading unverified
Skill content.

With a bundled archive, bootstrap installs the Skill at
`~/.local/share/tunlite/skills/use-tunlite`. A custom `--install-dir <prefix>/bin`
(or `TUNLITE_INSTALL_DIR`) uses `<prefix>/share/tunlite/skills/use-tunlite`; any
binary directory maps to its parent's `share` directory. Bootstrap prints the
actual local directory; Homebrew's installation hints use its stable `opt` path.
To enable it, tell your Agent: `Install and enable the use-tunlite Skill from
"<printed local directory>".` For legacy bootstrap installs, give the Agent the
printed same-version URL instead. Activation is optional and follows the Agent's
own installation mechanism; neither installer modifies Agent directories. After
upgrading, ask the Agent to refresh any copied Skill. A stable symlink is an option
only if the Agent supports it.

A manually downloaded native binary can be copied directly into `PATH`:

```bash
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

Installation, upgrades, and removal belong to the release channel, package manager,
or a future Desktop. Use `brew update && brew upgrade tunlite/tap/tunlite` and
`brew uninstall tunlite/tap/tunlite` for Homebrew. For source installs, repeat the
tagged checkout and install command with the new tag/version; remove only the executable
at your chosen installation destination to uninstall. Run `type -a tunlite` and
`tunlite version --json` after switching channels: an older copy earlier in `PATH`
can hide the new one. Choose one channel; installation does not migrate older dev
service records. The core CLI does not expose those commands or create a `tun`
alias. Replacing the CLI does not stop OS-owned SSH connections. The v1 preview does
not provide shell completion or inspect Agent directories. To use Tunlite from an
Agent, ask that Agent to install the official [`use-tunlite` Skill package](skill/use-tunlite/).

## Quick start

```bash
# Define local, remote, or SOCKS forwards with familiar OpenSSH syntax.
tunlite add db -L 5432:localhost:5432 deploy@app01
tunlite add preview -R 9000:localhost:3000 deploy@app01
tunlite add socks -D 1080 deploy@app01

# Run one connection attached to this terminal.
tunlite run db

# Or run it in the background for this login session.
tunlite start db
tunlite stop db

# Keep it running across future logins and start it now.
tunlite enable db
tunlite status db
tunlite logs db --lines 100

# On macOS, explicitly move the native service to system scope.
# This requires administrator authorization and starts it independently of login.
tunlite enable db --scope system

# Use system OpenSSH tools for key setup, then check authentication.
tunlite check db

# Resolve exceptional drift with an explicit source of truth.
tunlite resolve db --use tunlite --dry-run
tunlite resolve db --use system
```

SSH is the permanent default driver; `--driver ssh` is optional. Use `-p` for a
non-default port, `-i` for an identity, `-J` for jump hosts, and allowlisted
`-o Key=Value` options. The target is one explicit `host` or `user@host`; do not
append `:port`.

Preview usernames are intentionally strict: ASCII letters, digits, `.`, `_`, and
`-` only. UPN or multi-`@` names, names containing `+`, and `DOMAIN\user` are not
accepted, and there is no separate `-l` workaround. Accounts that require those
forms are outside this preview's SSH target grammar.

`set` changes only the supplied fields. To remove optional SSH fields, repeat the
single clear operation as needed:

```bash
tunlite set db --clear identity --clear jump --clear options
```

Both `--clear jump` and `--clear=jump` are accepted. The clearable fields are
exactly `identity`, `jump`, and `options`; there are no field-specific aliases. An unknown
field, a duplicate clear, or setting and clearing the same field in one command is
rejected. Supplying any `-L`/`-R`/`-D` or `-o` still replaces that complete list.

Tunlite SSH execution and checks deliberately use `ssh -F none`: they do not inherit
`~/.ssh/config` aliases, commands, hidden forwards, or multiplexing. Use an explicit
host and explicit safe arguments. A future SSH Config importer may snapshot safe
alias fields, but runtime behavior will remain represented by the Connection.

Persistent services are per connection:

```text
tunlite CLI -> SSH Driver -> ExecutionPlan -> systemd/launchd -> ssh
```

The generated service executes `ssh` directly. Once installed, a running connection
does not depend on the Tunlite binary remaining present.

### Service scope

Each Connection declaration exists exactly once. Service scope is machine-local
runtime ownership stored in the Service Record and native artifact; it is never
exported as part of the portable Connection.

- `tunlite enable <name>` defaults new services to `user` scope and preserves an
  existing service's scope.
- `tunlite enable <name> --scope user|system` explicitly selects or migrates scope;
  there is no separate `migrate` command.
- `user` needs no administrator access. On macOS, `system` maps to a launchd
  LaunchDaemon and requires administrator authorization. Linux currently supports
  systemd user services only and reports system scope as unsupported.
- In an interactive terminal, Tunlite explains the privileged change and runs one
  `sudo -v` after every read-only batch check succeeds and before the first write.
  `sudo` owns the password prompt; Tunlite does not read or store the password.
  If no interactive terminal is available, the command fails quickly with
  `privilege_required` and explains how to retry.
- `--non-interactive` never prompts or runs interactive authorization. System-scope
  automation must pre-authorize access that Tunlite's exact `sudo -n -- ...`
  platform commands can use; otherwise it returns `privilege_required`.
- `--dry-run` performs read-only planning without authorization or native-service writes.

An interactive migration shows its source, destination, privileged impact, and
progress. `--json` keeps stdout machine-only and reports stable per-item outcomes.

## Agent contract

Use `--json --non-interactive` for automation and inspect runtime capabilities instead
of scraping human output:

```bash
tunlite capabilities --json --non-interactive
tunlite schema cli --json --non-interactive
tunlite add db -L 5432:localhost:5432 deploy@app01 --dry-run --json --non-interactive
```

Automation must verify the integer `protocolVersion` returned by `capabilities`
before taking action. Raw Schemas and exported Connection documents are separate
documents: they use `$schema` and `configVersion`, not the CLI response envelope.
Use `schema --output -` or `export --output -` when raw JSON is required on stdout.

State has five independent observation dimensions: `serviceState`, `mode`,
`processState`, `health`, and `consistencyState`. The separate `scope` field is
`user` or `system` and identifies local service ownership. Batch operations return per-item results and use
exit code `5` for partial failure. Public listeners and publish plans produce
structured risk warnings; v1 does not maintain a separate approval state.

The standalone [`use-tunlite` Agent Skill](skill/use-tunlite/SKILL.md) is tested
against the same command, exit-code, state, and Schema contract exposed by the CLI.
Its installation and removal belong to the Agent, not to Tunlite.

## Configuration

Connections use a generic `Connection` envelope. SSH-specific fields live only under
the built-in `io.tunlite.ssh` driver config, leaving the core ready for future drivers
without making an untested public plugin protocol part of v1.

```bash
tunlite export --output tunlite.json
tunlite validate tunlite.json
tunlite import tunlite.json --dry-run
```

V1 writes `~/.config/tunlite/connections.json`. Import accepts only the documented
V1 `ConnectionList` format; unsupported versions and unknown fields are rejected.

## Preview scope

- Built-in SSH `-L`, `-R`, and `-D`, multiple forwards, jump hosts, identity files,
  strict OpenSSH `Key=Value` options, checks, diagnosis,
  logs, and drift repair.
- A native single binary; installation, upgrades, and removal belong to its
  distribution channel.
- Native persistent services on macOS 12+ (launchd user and system scopes) and Linux
  (systemd user scope; Linux system scope is not implemented).
- Windows is not published in the first preview. It will enter the support matrix
  only after the per-Connection Task Scheduler adapter passes real Windows validation.
  The repository still compiles for Windows, while Monitor rejects the unsupported
  platform before attempting to enter its terminal UI.
- External drivers and plugin installation are deliberately deferred until real user
  demand validates more than SSH.

The current hardware evidence is intentionally narrower than the build matrix. On
2026-08-31, Intel macOS passed the final Universal 2 build check, LaunchAgent release
E2E, and an isolated LaunchDaemon USER→SYSTEM→USER→SYSTEM lifecycle with real `-L`
traffic, system `set`, restart without sudo, disable/remove, and exact cleanup. Ubuntu
24.04 PID 1 systemd Docker E2E has passed. Apple Silicon hardware and non-container
Linux hosts remain unverified and are required before stable `v1.0.0`, not Preview.1.

The Chinese [product design](docs/PROJECT_DIRECTION.md) and
[architecture design](docs/ARCHITECTURE.md) are the two peer source documents for
v1 and later work.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
scripts/package-release.sh v1.0.0-preview.1
scripts/test-linux-docker-e2e.sh v1.0.0-preview.1
```

Real OpenSSH and native-service lifecycle tests are opt-in; see their source under
`internal/cli` and `internal/service`. Contributors should also read
[CONTRIBUTING.md](CONTRIBUTING.md) and the long-term
[Git governance policy](docs/GIT_GOVERNANCE.md).

MIT License.
