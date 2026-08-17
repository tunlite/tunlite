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

# Windows PowerShell (foreground CLI in the first preview)
irm https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.ps1 | iex
```

Every release contains self-contained archives and `SHA256SUMS`. See the
[preview guide](docs/V1_PREVIEW.md) for manual installation, supported platforms,
artifact verification, and migration from 0.10.3.

## Quick start

```bash
# Define local, remote, or SOCKS forwards. Flags are repeatable.
tunlite add db --to deploy@app01 -L 5432:localhost:5432
tunlite add preview --to deploy@app01 -R 9000:localhost:3000
tunlite add socks --to deploy@app01 -D 1080

# Run one connection attached to this terminal.
tunlite run db

# Or let the native service manager keep it running and restart it on failure.
tunlite enable db
tunlite status db
tunlite logs db --lines 100

# Detect and repair drift between the declaration and native service.
tunlite sync db --dry-run
tunlite sync db
```

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
tunlite capabilities --json
tunlite schema cli
tunlite add db --to deploy@app01 -L 5432:localhost:5432 --dry-run --json --non-interactive
```

State is intentionally four-dimensional: `desiredState`, `processState`, `health`,
and `syncState`. Batch operations return per-item results and use exit code `6` for
partial failure. Public listeners and publish plans require explicit approval bound
to the current invocation or persistent plan.

The bundled [SSH Agent Skill](skill/ssh-tunnel/SKILL.md) is tested against the same
command, exit-code, state, and Schema contract exposed by the CLI.

## Configuration and migration

Connections use a generic `Connection` envelope. SSH-specific fields live only under
the built-in `io.tunlite.ssh` driver config, leaving the core ready for future drivers
without making an untested public plugin protocol part of v1.

```bash
tunlite export --output tunlite.json
tunlite validate tunlite.json
tunlite import tunlite.json --dry-run

# After stopping and removing the 0.10.3 service:
tunlite migrate --dry-run --json --non-interactive
tunlite migrate --json --non-interactive
```

Migration saves a byte-for-byte, checksum-addressed backup. `migrate --rollback`
refuses to overwrite a v1 config changed after migration.

## Preview scope

- Built-in SSH `-L`, `-R`, and `-D`, multiple forwards, jump hosts, identity files,
  strict OpenSSH `Key=Value` options, checks, diagnosis, logs, and drift repair.
- Native persistent services on macOS 12+ (launchd) and Linux (systemd user services).
- Windows amd64 supports the foreground/configuration CLI; persistent services are
  not part of the first preview.
- External drivers and plugin installation are deliberately deferred until real user
  demand validates more than SSH.

The Chinese [project direction](docs/PROJECT_DIRECTION.md) is the sole product and
architecture guide for v1 and later work.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
scripts/package-release.sh v1.0.0-preview.1
```

Real OpenSSH and native-service lifecycle tests are opt-in; see their source under
`internal/cli` and `internal/service`.

MIT License.
