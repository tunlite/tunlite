'use strict';

// Windows adapter: Task Scheduler logon task (primary), with a Startup-folder
// shortcut command available as documentation/fallback.

const fs = require('fs');
const os = require('os');
const path = require('path');
const cp = require('child_process');

function taskName() { return 'tunlite'; }

// The command Task Scheduler runs at logon. Quoted for cmd parsing.
function runCommand(ctx) {
  return `"${ctx.nodePath}" "${ctx.entry}" daemon run`;
}

function render(ctx) {
  // Returned for inspection/testing; the actual install uses schtasks.
  return {
    path: `Task Scheduler\\${taskName()}`,
    content: runCommand(ctx),
  };
}

function install(ctx) {
  fs.mkdirSync(ctx.logDir, { recursive: true });
  const args = [
    '/Create',
    '/TN', taskName(),
    '/TR', runCommand(ctx),
    '/SC', 'ONLOGON',
    '/RL', 'LIMITED',
    '/F',
  ];
  const r = cp.spawnSync('schtasks', args, { encoding: 'utf8' });
  return { path: `Task Scheduler\\${taskName()}`, ok: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
}

// Task Scheduler runs our task ONLOGON, not as a managed service, so there's no
// OS-owned "restart the daemon now" primitive to delegate to. Report unsupported;
// callers fall back to their no-service path.
function restart() {
  return { ok: false, unsupported: true };
}

function uninstall() {
  const r = cp.spawnSync('schtasks', ['/Delete', '/TN', taskName(), '/F'], { encoding: 'utf8' });
  return { path: `Task Scheduler\\${taskName()}`, ok: r.status === 0, removed: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
}

function status() {
  const r = cp.spawnSync('schtasks', ['/Query', '/TN', taskName()], { encoding: 'utf8' });
  const installed = r.status === 0;
  const running = installed && /Running/i.test(r.stdout || '');
  return { platform: 'taskscheduler', installed, running, path: `Task Scheduler\\${taskName()}` };
}

function startupShortcutDir() {
  return path.join(os.homedir(), 'AppData', 'Roaming', 'Microsoft', 'Windows', 'Start Menu', 'Programs', 'Startup');
}

module.exports = { render, install, apply: restart, restart, uninstall, status, taskName, runCommand, startupShortcutDir };
