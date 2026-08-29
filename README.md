# Tunlite

> Reliable SSH connections for developers and AI agents.

**English** · [简体中文](README.zh-CN.md)

[![Go CI](https://github.com/yuanyuanzijin/tunlite/actions/workflows/go-ci.yml/badge.svg)](https://github.com/yuanyuanzijin/tunlite/actions/workflows/go-ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Tunlite turns OpenSSH port forwards into named connections that can run in the
foreground or as isolated operating-system services. It uses the `ssh` already on
your machine, creates no account or server, and does not invent a transport protocol.

The v1 preview is a single Go binary. There is no central background controller:
each persistent connection is owned directly by systemd or launchd, so one failing
connection does not take the others down.

## Install the preview

The system `ssh` command is the only runtime prerequisite.

```bash
# macOS / Linux; downloads v1.0.0-preview.1 and verifies SHA-256
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh
```

Every release contains self-contained archives and `SHA256SUMS`. Follow the
[zero-to-acceptance guide](docs/INSTALL_AND_ACCEPTANCE.md) for installation and
platform verification. See the [preview guide](docs/V1_PREVIEW.md) for current
version boundaries and release gates.

A manually downloaded native binary can be copied directly into `PATH`:

```bash
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

Installation, upgrades, and removal belong to the release channel, package manager,
or a future Desktop. The core CLI does not expose those commands or create a `tun`
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

State is intentionally five-dimensional: `serviceState`, `mode`, `processState`,
`health`, and `consistencyState`. Batch operations return per-item results and use
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
- Native persistent services on macOS 12+ (launchd) and Linux (systemd user services).
- Windows is not published in the first preview. It will enter the support matrix
  only after the per-Connection Task Scheduler adapter passes real Windows validation.
- External drivers and plugin installation are deliberately deferred until real user
  demand validates more than SSH.

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
