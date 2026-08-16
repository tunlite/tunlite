'use strict';

// Linux systemd (user) service adapter.

const fs = require('fs');
const os = require('os');
const path = require('path');
const cp = require('child_process');

// Unit name is derived from ctx (ctx.systemdUnit, computed in index.js context())
// so an explicit ctx is honored — mirrors launchd reading ctx.label. Falls back
// to the default when called without a ctx.
function unitName(ctx) { return `${(ctx && ctx.systemdUnit) || 'tunlite'}.service`; }

function unitPath(ctx) {
  const dir = (ctx && ctx.systemdDir) || process.env.TUNLITE_SYSTEMD_DIR;
  if (dir) return path.join(dir, unitName(ctx));
  const base = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config');
  return path.join(base, 'systemd', 'user', unitName(ctx));
}

function render(ctx) {
  const content = `[Unit]
Description=tunlite SSH tunnel manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${ctx.nodePath} ${ctx.entry} daemon run
Restart=always
RestartSec=5
Environment=PATH=${ctx.pathEnv}

[Install]
WantedBy=default.target
`;
  return { path: unitPath(ctx), content };
}

function userctl(args, opts = {}) {
  return cp.spawnSync('systemctl', ['--user', ...args], { encoding: 'utf8', ...opts });
}

function install(ctx, run = cp.spawnSync) {
  const uctl = (args) => run('systemctl', ['--user', ...args], { encoding: 'utf8' });
  const { path: file, content } = render(ctx);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.mkdirSync(ctx.logDir, { recursive: true });
  fs.writeFileSync(file, content);
  uctl(['daemon-reload']);
  const en = uctl(['enable', unitName(ctx)]); // link for boot (no --now)
  const r = restart(ctx, run); // apply new code via an init-owned restart (see below)
  return {
    path: file,
    ok: en.status === 0 && r.ok,
    output: (r.output || (en.stderr || en.stdout || '')).trim(),
    note: 'For tunnels to start before login, run: loginctl enable-linger',
  };
}

// Restart the unit, owned by the systemd user manager — the shared "apply new
// code" primitive for both reinstall and `update`. `restart` (not `enable --now`,
// which no-ops on an already-running unit) forces the freshly anchored code to
// load; and because systemd owns the restart, a severed caller session can't
// strand the daemon (Restart=always is the backstop). It also starts a
// not-yet-running unit, covering the fresh-install case.
function restart(ctx, run = cp.spawnSync) {
  const r = run('systemctl', ['--user', 'restart', unitName(ctx)], { encoding: 'utf8' });
  return { ok: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
}

function uninstall(ctx) {
  const file = unitPath(ctx);
  userctl(['disable', '--now', unitName(ctx)]);
  let removed = false;
  try { fs.unlinkSync(file); removed = true; } catch (_) {}
  userctl(['daemon-reload']);
  return { path: file, ok: true, removed };
}

function status(ctx) {
  const file = unitPath(ctx);
  const installed = fs.existsSync(file);
  const active = userctl(['is-active', unitName(ctx)]);
  const enabled = userctl(['is-enabled', unitName(ctx)]);
  return {
    platform: 'systemd',
    installed,
    running: (active.stdout || '').trim() === 'active',
    enabled: (enabled.stdout || '').trim() === 'enabled',
    path: file,
  };
}

module.exports = { render, install, apply: install, restart, uninstall, status, unitPath, unitName };
