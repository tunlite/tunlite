'use strict';

// macOS launchd LaunchAgent adapter.

const fs = require('fs');
const os = require('os');
const path = require('path');
const cp = require('child_process');

function plistPath(ctx) {
  const dir = (ctx && ctx.launchAgentsDir) || path.join(os.homedir(), 'Library', 'LaunchAgents');
  return path.join(dir, `${ctx.label}.plist`);
}

function render(ctx) {
  const args = [ctx.nodePath, ctx.entry, 'daemon', 'run'];
  const argXml = args.map((a) => `      <string>${escapeXml(a)}</string>`).join('\n');
  const content = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>${ctx.label}</string>
    <key>ProgramArguments</key>
    <array>
${argXml}
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>EnvironmentVariables</key>
    <dict>
      <key>PATH</key>
      <string>${escapeXml(ctx.pathEnv)}</string>
    </dict>
    <key>StandardOutPath</key>
    <string>${escapeXml(path.join(ctx.logDir, 'launchd.out.log'))}</string>
    <key>StandardErrorPath</key>
    <string>${escapeXml(path.join(ctx.logDir, 'launchd.err.log'))}</string>
  </dict>
</plist>
`;
  return { path: plistPath(ctx), content };
}

// Is our launchd job already bootstrapped (i.e. this is a reinstall)? `print`
// exits 0 only for a loaded label. `run` is injectable so install() is testable
// without touching the real launchd.
function isLoaded(ctx, run) {
  const uid = process.getuid ? process.getuid() : 0;
  return run('launchctl', ['print', `gui/${uid}/${ctx.label}`], { stdio: 'ignore' }).status === 0;
}

function install(ctx, run = cp.spawnSync) {
  const { path: file, content } = render(ctx);
  let previous = null;
  try { previous = fs.readFileSync(file, 'utf8'); } catch (_) {}
  const loaded = isLoaded(ctx, run);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.mkdirSync(ctx.logDir, { recursive: true });
  fs.writeFileSync(file, content);
  const uid = process.getuid ? process.getuid() : 0;
  const domain = `gui/${uid}`;

  // Reinstall over an already-loaded job: NEVER `unload` then `load`. Those are
  // two non-atomic steps and `unload` stops the daemon — which SIGTERMs every
  // ssh child, dropping all tunnels. If the reinstall is itself running over a
  // tunlite tunnel, that drop severs its own session and kills the reinstall
  // process BEFORE the matching `load` runs → old job gone, new job never loaded
  // → daemon stranded, locked out. `kickstart -k` makes launchd kill+relaunch
  // the job itself (launchd owns the relaunch, so a severed session can't strand
  // it), and the relaunched daemon picks up the freshly anchored code.
  if (loaded) {
    // kickstart restarts the in-memory launchd definition; it does NOT reread a
    // changed plist. Most updates keep the same pinned entry and only replace
    // its files, so kickstart is the safest fast path. If the executable/log
    // paths changed (for example after replacing an nvm-managed Node), hand a
    // bootout+bootstrap to a detached helper that survives this caller's tunnel
    // dropping and rolls the old plist back if the new one cannot load.
    if (definitionChanged(previous, content)) {
      return { path: file, ...reloadDefinition(ctx, file, previous) };
    }
    return { path: file, ...restart(ctx, run) };
  }

  // Fresh install (job not loaded): bootstrap (modern), fall back to legacy load.
  // No `unload` first — there's nothing loaded to unload.
  let r = run('launchctl', ['bootstrap', domain, file], { encoding: 'utf8' });
  if (r.status !== 0) r = run('launchctl', ['load', '-w', file], { encoding: 'utf8' });
  return { path: file, ok: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
}

function reloadDefinition(ctx, file, previous, spawn = cp.spawn) {
  const uid = process.getuid ? process.getuid() : 0;
  const domain = `gui/${uid}`;
  const target = `${domain}/${ctx.label}`;
  const backup = `${file}.previous`;
  if (previous !== null) fs.writeFileSync(backup, previous);
  else { try { fs.unlinkSync(backup); } catch (_) {} }

  try {
    const helper = path.join(__dirname, 'launchd-reload.js');
    const child = spawn(process.execPath, [helper, target, domain, file, backup], {
      detached: true,
      stdio: 'ignore',
    });
    child.unref();
    return { ok: true, reloading: true, output: '' };
  } catch (error) {
    if (previous !== null) fs.writeFileSync(file, previous);
    return { ok: false, reloading: false, output: error.message };
  }
}

// Restart the loaded job, owned by launchd: `kickstart -k` kills and relaunches
// it in one call, so a severed caller session can't strand the daemon. This is
// the shared "apply new code" primitive used by both reinstall and `update`.
function restart(ctx, run = cp.spawnSync) {
  const uid = process.getuid ? process.getuid() : 0;
  const r = run('launchctl', ['kickstart', '-k', `gui/${uid}/${ctx.label}`], { encoding: 'utf8' });
  return { ok: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
}

function uninstall(ctx) {
  const file = plistPath(ctx);
  const uid = process.getuid ? process.getuid() : 0;
  let r = cp.spawnSync('launchctl', ['bootout', `gui/${uid}/${ctx.label}`], { encoding: 'utf8' });
  if (r.status !== 0) {
    cp.spawnSync('launchctl', ['unload', '-w', file], { stdio: 'ignore' });
  }
  let removed = false;
  try { fs.unlinkSync(file); removed = true; } catch (_) {}
  return { path: file, ok: true, removed };
}

// Best-effort parse of `launchctl print` human output, which Apple reshapes
// across macOS versions. Treat the service as running if it self-reports
// `state = running` OR exposes a live `pid = <number>` line — a real pid means
// the process is up even when the state wording has changed.
function parseRunning(stdout) {
  const s = stdout || '';
  return /state = running/.test(s) || /\bpid = \d+/.test(s);
}

function status(ctx, run = cp.spawnSync) {
  const file = plistPath(ctx);
  const installed = fs.existsSync(file);
  const uid = process.getuid ? process.getuid() : 0;
  const r = run('launchctl', ['print', `gui/${uid}/${ctx.label}`], { encoding: 'utf8' });
  const loaded = r.status === 0;
  const running = loaded && parseRunning(r.stdout);
  return { platform: 'launchd', installed, loaded, running, path: file };
}

function escapeXml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function definitionChanged(before, after) {
  const a = definitionSignature(before);
  const b = definitionSignature(after);
  return a === null || b === null || a !== b;
}

function definitionSignature(content) {
  if (!content) return null;
  const args = /<key>ProgramArguments<\/key>\s*<array>([\s\S]*?)<\/array>/.exec(content);
  if (!args) return null;
  const values = [...args[1].matchAll(/<string>([\s\S]*?)<\/string>/g)].map((m) => unescapeXml(m[1]));
  const out = valueForKey(content, 'StandardOutPath');
  const err = valueForKey(content, 'StandardErrorPath');
  return JSON.stringify({ values, out, err });
}

function valueForKey(content, key) {
  const match = new RegExp(`<key>${key}<\\/key>\\s*<string>([\\s\\S]*?)<\\/string>`).exec(content);
  return match ? unescapeXml(match[1]) : null;
}

function unescapeXml(s) {
  return String(s).replace(/&gt;/g, '>').replace(/&lt;/g, '<').replace(/&amp;/g, '&');
}

module.exports = {
  render, install, apply: install, restart, reloadDefinition, uninstall, status, plistPath,
  parseRunning, isLoaded, definitionChanged, definitionSignature,
};
