# Contributing to tunlite

Thanks for your interest in improving tunlite! This document covers everything
you need to get started.

Before proposing architectural or v1 work, read the accepted
[Project Direction](docs/PROJECT_DIRECTION.md). It is the sole source of truth
for the SSH-focused Go v1, daemon removal, native service architecture, and the
extension boundaries reserved for future drivers. The constraints below
describe the currently released 0.x Node.js implementation where they differ.

## Project philosophy

tunlite is deliberately small. Please keep these constraints in mind:

- **Zero runtime dependencies.** tunlite uses only the Node.js standard library
  and the system `ssh`. Pull requests that add an `npm` dependency will almost
  always be declined — propose the idea in an issue first.
- **Cross-platform.** Code should work on macOS, Linux, and Windows (Windows
  support is currently beta — less verified, not yet covered by CI). Guard
  platform-specific behavior behind `process.platform` checks.
- **Agent- and script-friendly.** Commands support `--json` and use stable exit
  codes. Preserve that contract when changing output.

## Development setup

```bash
git clone https://github.com/yuanyuanzijin/tunlite.git
cd tunlite
node bin/tunlite.js --help   # run it straight from source — no install needed
```

Requirements: **Node.js ≥ 18**. There is nothing to `npm install` (no
dependencies).

## Running the tests

```bash
node --test          # or: npm test
```

The suite runs entirely offline and never touches your real
system:

- A fake `ssh` (`fixtures/fake-ssh.js`) is injected via the `TUNLITE_SSH`
  environment variable, so no real SSH connections are made.
- Autostart/service code is neutralized via `TUNLITE_FAKE_AUTOSTART=1`, so no
  real `launchd` / `systemd` / Task Scheduler entries are created.

Please add or update tests for any behavior you change.

## Submitting changes

1. Fork the repository and create a branch from the default branch.
2. Make your change. Match the style of the surrounding code.
3. Make sure `node --test` passes.
4. Open a pull request with a clear description of **what** changed and **why**.
   Link any related issue (e.g. `Closes #123`).

Clear, conventional commit messages (`feat:`, `fix:`, `docs:`, …) are
appreciated but not required.

## Reporting bugs and security issues

- **Bugs / feature requests:** open an [issue](https://github.com/yuanyuanzijin/tunlite/issues).
- **Security vulnerabilities:** please report privately — see [SECURITY.md](SECURITY.md).

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).
