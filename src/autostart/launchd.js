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
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.mkdirSync(ctx.logDir, { recursive: true });
  fs.writeFileSync(file, content);
  const uid = process.getuid ? process.getuid() : 0;
  const domain = `gui/${uid}`;
  const target = `${domain}/${ctx.label}`;

  // Reinstall over an already-loaded job: NEVER `unload` then `load`. Those are
  // two non-atomic steps and `unload` stops the daemon — which SIGTERMs every
  // ssh child, dropping all tunnels. If the reinstall is itself running over a
  // tunlite tunnel, that drop severs its own session and kills the reinstall
  // process BEFORE the matching `load` runs → old job gone, new job never loaded
  // → daemon stranded, locked out. `kickstart -k` makes launchd kill+relaunch
  // the job itself (launchd owns the relaunch, so a severed session can't strand
  // it), and the relaunched daemon picks up the freshly anchored code.
  if (isLoaded(ctx, run)) return { path: file, ...restart(ctx, run) };

  // Fresh install (job not loaded): bootstrap (modern), fall back to legacy load.
  // No `unload` first — there's nothing loaded to unload.
  let r = run('launchctl', ['bootstrap', domain, file], { encoding: 'utf8' });
  if (r.status !== 0) r = run('launchctl', ['load', '-w', file], { encoding: 'utf8' });
  return { path: file, ok: r.status === 0, output: (r.stderr || r.stdout || '').trim() };
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

function status(ctx) {
  const file = plistPath(ctx);
  const installed = fs.existsSync(file);
  const uid = process.getuid ? process.getuid() : 0;
  const r = cp.spawnSync('launchctl', ['print', `gui/${uid}/${ctx.label}`], { encoding: 'utf8' });
  const running = r.status === 0 && parseRunning(r.stdout);
  return { platform: 'launchd', installed, running, path: file };
}

function escapeXml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

module.exports = { render, install, restart, uninstall, status, plistPath, parseRunning, isLoaded };
