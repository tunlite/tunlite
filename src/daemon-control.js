'use strict';

// Daemon-process lifecycle (ping / ensure-running / restart-for-new-code) plus
// the self-update plumbing (tarball fetch + update rendering). These manage the
// supervisor PROCESS, distinct from the in-daemon tunnel reconcile. Imports from
// cli-core for shared primitives; never requires ./cli.

const cp = require('child_process');
const os = require('os');
const fs = require('fs');
const path = require('path');
const paths = require('./paths');
const ipc = require('./ipc');
const autostart = require('./autostart');
const { EXIT, line, sleep } = require('./cli-core');

const ENTRY = path.join(__dirname, '..', 'bin', 'tunlite.js');

async function daemonPing(socketPath = paths.socketPath()) {
  try { return await ipc.request(socketPath, 'ping', {}, { timeoutMs: 1500 }); }
  catch (_) { return null; }
}

async function ensureDaemon(io) {
  const sock = paths.socketPath();
  const ping = await daemonPing(sock);
  if (ping) return ping;
  // Start detached so tunnels keep running after the CLI exits. windowsHide
  // keeps the detached daemon from opening its own console window on Windows
  // (detached children get one by default).
  const child = cp.spawn(process.execPath, [ENTRY, 'daemon', 'run'], {
    detached: true, stdio: 'ignore', windowsHide: true,
  });
  child.unref();
  for (let i = 0; i < 40; i++) {
    const p = await daemonPing(sock);
    if (p) { line(io, `daemon started (pid ${p.pid})`); return p; }
    await sleep(100);
  }
  throw Object.assign(new Error('daemon failed to start'), { exitCode: EXIT.DAEMON });
}

// Fetch a runtime tarball (no git, no npm) into a temp dir; returns the dir.
// Honors TUNLITE_ARCHIVE_URL for offline/mirror use; otherwise builds the
// GitHub `/archive/<ref>.tar.gz` URL from the repo url (works for branches and
// tags). Tries curl, then
// wget, then extracts with tar (--strip-components=1 drops the top dir).
function archiveFetch(repoUrl, tag) {
  const ref = tag || 'master';
  const base = require('./update').httpsRepoUrl(repoUrl).replace(/\.git$/, '');
  const url = process.env.TUNLITE_ARCHIVE_URL || `${base}/archive/${ref}.tar.gz`;
  // Only fetch over an encrypted transport (https) or a local file (no network).
  // Reject http and every other scheme so a MITM or a hostile mirror can't
  // downgrade the self-update channel and feed arbitrary code that then gets
  // anchored and run as the pinned entrypoint.
  if (!/^https:\/\//i.test(url) && !/^file:\/\//i.test(url)) {
    throw Object.assign(
      new Error(`refusing to fetch update over insecure URL "${url}" (only https:// or file:// allowed)`),
      { exitCode: EXIT.ERROR },
    );
  }
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'tunlite-update-'));
  const tgz = path.join(dir, 'archive.tar.gz');
  try {
    // Download to a FILE, never a captured stdout buffer. The GitHub archive is
    // the whole repo (docs, recordings, tests, …) and can exceed spawnSync's 1 MB
    // default maxBuffer, which SIGTERMs the fetcher with ENOBUFS — surfacing as a
    // bogus "need curl or wget" even though both are installed and reachable.
    const tools = [
      ['curl', ['-fsSL', '-o', tgz, url]],
      ['wget', ['-q', '-O', tgz, url]],
    ];
    let ok = false;
    let detail = ''; // last real failure (absent tools are skipped, not reported)
    for (const [bin, args] of tools) {
      const r = cp.spawnSync(bin, args, { encoding: 'utf8' });
      if (r.error && r.error.code === 'ENOENT') continue; // tool not installed — try the next
      if (!r.error && r.status === 0) { ok = true; break; }
      detail = ((r.stderr && r.stderr.trim()) || (r.error && r.error.message) || `${bin} exited ${r.status}`);
    }
    if (!ok) {
      throw Object.assign(
        new Error(detail ? `could not download ${url} — ${detail}` : `could not download ${url} (need curl or wget)`),
        { exitCode: EXIT.ERROR },
      );
    }
    const tar = cp.spawnSync('tar', ['xz', '--strip-components=1', '-C', dir, '-f', tgz], { encoding: 'utf8' });
    if (tar.error || tar.status !== 0) throw new Error('failed to extract update tarball (need tar)');
    fs.rmSync(tgz, { force: true }); // drop the archive so only the unpacked tree remains
    return dir;
  } catch (e) {
    fs.rmSync(dir, { recursive: true, force: true });
    throw e;
  }
}

// Resolve "latest" to the newest published tag via the GitHub tags API, so a
// self-update only ever targets a real release (never the branch tip, which
// could be an unreleased commit npm doesn't have). Returns null when a
// TUNLITE_ARCHIVE_URL is pinned (use that source verbatim); throws with a clear
// hint when GitHub can't be reached or has no usable tag.
function resolveLatestTag(repoUrl) {
  if (process.env.TUNLITE_ARCHIVE_URL) return null; // source is pinned — don't second-guess it
  const updateMod = require('./update');
  const base = updateMod.httpsRepoUrl(repoUrl).replace(/\.git$/, '');
  const m = /github\.com\/([^/]+)\/([^/]+?)$/.exec(base);
  if (!m) throw Object.assign(new Error(`can't determine the latest release from "${base}" — pass a version: tunlite update X.Y.Z`), { exitCode: EXIT.ERROR });
  const api = `https://api.github.com/repos/${m[1]}/${m[2]}/tags?per_page=100`;
  const ua = 'tunlite';
  let r = cp.spawnSync('curl', ['-fsSL', '-H', `User-Agent: ${ua}`, '-H', 'Accept: application/vnd.github+json', api], { encoding: 'utf8' });
  if (r.error || r.status !== 0) {
    r = cp.spawnSync('wget', ['-qO-', `--header=User-Agent: ${ua}`, '--header=Accept: application/vnd.github+json', api], { encoding: 'utf8' });
    if (r.error || r.status !== 0) throw Object.assign(new Error('could not reach GitHub to find the latest release — check your connection or pass a version: tunlite update X.Y.Z'), { exitCode: EXIT.ERROR });
  }
  let names;
  try { names = JSON.parse(r.stdout).map((t) => t && t.name).filter(Boolean); }
  catch (_) { throw Object.assign(new Error('unexpected response from the GitHub tags API'), { exitCode: EXIT.ERROR }); }
  const tag = updateMod.pickLatestTag(names);
  if (!tag) throw Object.assign(new Error('no published release found to update to'), { exitCode: EXIT.ERROR });
  return tag;
}

// Restart the daemon PROCESS so it loads new code (distinct from commands.restart,
// which restarts tunnels in the running daemon). Either way the relaunch is owned
// by something that outlives THIS process — so a restart that drops a tunnel
// carrying our own ssh session can't strand the daemon. With an OS service: the
// service manager restarts it (the same primitive a reinstall uses). Without one:
// the daemon relaunches ITSELF via a detached successor (shutdown {relaunch:true}).
async function restartDaemonProcess(io) {
  const before = await daemonPing();
  if (!before) return { restarted: false, reason: 'not-running' };
  const oldPid = before.pid;
  const ctx = autostart.context();
  let adapter = null;
  let serviceInstalled = false;
  try {
    adapter = autostart.adapterFor();
    const status = adapter.status(ctx);
    // A leftover launchd plist is not a managed service: only a loaded Job can
    // own the restart. Other adapters do not expose `loaded`, so their installed
    // state remains the best available signal.
    serviceInstalled = status.loaded === undefined ? status.installed : status.loaded;
  } catch (_) {}
  line(io, 'restarting daemon to apply new code (tunnels blip ~1s)…');

  if (serviceInstalled && adapter) {
    // Converge the service definition before restarting it. An update can select
    // a different stable Node path; a bare restart would then keep launchd's or
    // systemd's stale in-memory command. apply() rewrites/reloads the definition
    // when needed and leaves the actual relaunch owned by the OS service manager,
    // so it still survives this caller's tunnel dropping.
    const r = adapter.apply ? adapter.apply(ctx) : adapter.restart(ctx);
    if (r && r.unsupported) return spawnRelaunch(); // e.g. Windows Task Scheduler
    if (!r || !r.ok) return { restarted: false, reason: 'service-restart-failed', output: r && r.output };
    for (let i = 0; i < 100; i++) {
      const p = await daemonPing();
      if (p && p.pid !== oldPid) return { restarted: true, via: 'service', pid: p.pid, version: p.version };
      await sleep(100);
    }
    return { restarted: false, reason: 'service-relaunch-timeout' };
  }

  return spawnRelaunch();

  // No OS service to own the relaunch. Do NOT shut the daemon down and respawn it
  // from HERE: if this process rides a tunnel the restart drops, it dies before
  // the respawn → daemon stranded, locked out. Instead tell the daemon to relaunch
  // ITSELF — it spawns a fresh, detached successor (new code) as it exits, owned by
  // the daemon, not by our (possibly tunnel-carried) session.
  async function spawnRelaunch() {
    try { await ipc.request(paths.socketPath(), 'shutdown', { relaunch: true }); }
    catch (_) { return { restarted: false, reason: 'shutdown-request-failed' }; }
    for (let i = 0; i < 100; i++) {
      const p = await daemonPing();
      if (p && p.pid !== oldPid) return { restarted: true, via: 'self-relaunch', pid: p.pid, version: p.version };
      await sleep(100);
    }
    return { restarted: false, reason: 'self-relaunch-timeout' };
  }
}

function renderUpdate(io, res) {
  switch (res.action) {
    case 'refused': {
      const how = {
        git: 'this is a git checkout — update with `git pull`, then re-run `tunlite install`',
        'npm-global': 'tunlite was installed globally with npm — update with `npm i -g tunlite@latest` (or `npx tunlite@latest install`)',
        unmanaged: 'this is not an anchored install — (re)install with `npx tunlite install`',
      }[res.reason] || 'not an anchored install — (re)install with `npx tunlite install`';
      line(io, `not self-updating: ${how}`);
      return;
    }
    case 'check':
      line(io, `current ${res.current}, ${res.explicit ? 'requested' : 'latest'} ${res.target} — ${res.available ? 'would update' : 'up to date'}`);
      return;
    case 'up-to-date':
      line(io, res.explicit ? `already on ${res.current}` : `already on the latest (${res.current})`);
      return;
    case 'updated': {
      const r = res.restarted;
      let tail;
      if (res.restartSkipped) tail = ' (daemon not restarted; new code applies on next start)';
      else if (r && r.restarted) tail = ` — daemon restarted (pid ${r.pid})`;
      else if (r && r.reason === 'not-running') tail = ' (daemon was not running)';
      else if (r && r.reason === 'service-relaunch-timeout') tail = ' — daemon did not return within 10s; the OS service should relaunch it shortly (check: tunlite status)';
      else tail = ' — daemon restart did not confirm; run: tunlite daemon start';
      line(io, `updated ${res.from} -> ${res.to}${tail}`);
      return;
    }
    default:
      line(io, JSON.stringify(res));
  }
}

module.exports = { daemonPing, ensureDaemon, archiveFetch, resolveLatestTag, restartDaemonProcess, renderUpdate };
