'use strict';

const os = require('os');
const path = require('path');
const paths = require('../paths');

const launchd = require('./launchd');
const systemd = require('./systemd');
const windows = require('./windows');

// Context describing how the daemon should be launched by the OS service mgr.
// Prefer the install manifest's pinned node + lib entry (so a launchd/systemd
// launch — which has no nvm/fnm shims on PATH — uses a stable node and the
// anchored copy, not whatever transient node ran `install`). Fall back to the
// current node + the dev-tree entry when there's no manifest.
function context(opts = {}) {
  let m = null;
  try { m = require('../install').readManifest(); } catch (_) {}
  const entry = opts.entry
    || (m && m.libDir && path.join(m.libDir, 'bin', 'tunlite.js'))
    || path.join(__dirname, '..', '..', 'bin', 'tunlite.js');
  return {
    // Label and dirs are env-overridable so a sandboxed install gets its own
    // service identity and can never touch the real one.
    label: process.env.TUNLITE_SERVICE_LABEL || 'io.github.yuanyuanzijin.tunlite',
    name: 'tunlite',
    // systemd uses a short unit name (not the reverse-DNS launchd label). A
    // sandboxed install isolates it via TUNLITE_SERVICE_LABEL just like launchd;
    // with no override it stays the short default. Computed here (the single
    // source of truth) so the systemd adapter derives it from ctx, never the env.
    systemdUnit: process.env.TUNLITE_SERVICE_LABEL || 'tunlite',
    nodePath: opts.nodePath || (m && m.nodePath) || process.execPath,
    entry,
    logDir: opts.logDir || paths.logDir(),
    home: os.homedir(),
    launchAgentsDir: process.env.TUNLITE_LAUNCH_AGENTS_DIR || null,
    systemdDir: process.env.TUNLITE_SYSTEMD_DIR || null,
    pathEnv: process.env.PATH || '',
  };
}

// A no-op adapter so tests (and dry-runs) never touch the real OS service
// manager. Real plist/unit paths and `launchctl`/`systemctl` target the live
// system regardless of TUNLITE_HOME, so this guard prevents destructive side
// effects when TUNLITE_FAKE_AUTOSTART is set.
const sandbox = {
  render: () => ({ path: '(sandbox)', content: '' }),
  install: () => ({ path: '(sandbox)', ok: true, sandbox: true }),
  apply: () => ({ ok: true, sandbox: true }),
  restart: () => ({ ok: true, sandbox: true }),
  uninstall: () => ({ path: '(sandbox)', ok: true, removed: false, sandbox: true }),
  status: () => ({ platform: 'sandbox', installed: false, running: false }),
};

function adapterFor(platform = os.platform()) {
  if (process.env.TUNLITE_FAKE_AUTOSTART) return sandbox;
  if (platform === 'darwin') return launchd;
  if (platform === 'linux') return systemd;
  if (platform === 'win32') return windows;
  throw new Error(`unsupported platform for service install: ${platform}`);
}

module.exports = { context, adapterFor, sandbox, launchd, systemd, windows };
