#!/usr/bin/env node
'use strict';

// Real-machine regression check for the 0.10.3 restart hand-off. It uses a
// unique launchd label and TUNLITE_HOME, never the user's real Tunlite job.

const assert = require('assert/strict');
const cp = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const autostart = require('../src/autostart');
const launchd = require('../src/autostart/launchd');
const ipc = require('../src/ipc');
const paths = require('../src/paths');
const { daemonPing, ensureDaemon, restartDaemonProcess } = require('../src/daemon-control');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(fn, message, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await fn();
    if (value) return value;
    await sleep(100);
  }
  throw new Error(message);
}

function readStarts(file) {
  try {
    return fs.readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  } catch (_) {
    return [];
  }
}

function writeWorker(file, startsFile, kind) {
  fs.writeFileSync(file, [
    "'use strict';",
    "const fs = require('fs');",
    `fs.appendFileSync(${JSON.stringify(startsFile)}, JSON.stringify({ pid: process.pid, kind: ${JSON.stringify(kind)}, ts: Date.now() }) + '\\n');`,
    "process.on('SIGTERM', () => process.exit(0));",
    'setInterval(() => {}, 1000);',
    '',
  ].join('\n'));
  const checked = cp.spawnSync(process.execPath, ['--check', file], { encoding: 'utf8' });
  assert.equal(checked.status, 0, checked.stderr || `invalid worker: ${file}`);
}

function writeDaemonWrapper(file, startsFile, kind, env) {
  const entry = path.join(__dirname, '..', 'bin', 'tunlite.js');
  fs.writeFileSync(file, [
    "'use strict';",
    "const fs = require('fs');",
    `process.env.TUNLITE_HOME = ${JSON.stringify(env.home)};`,
    `process.env.TUNLITE_SOCKET = ${JSON.stringify(env.socket)};`,
    `process.env.TUNLITE_SERVICE_LABEL = ${JSON.stringify(env.label)};`,
    `process.env.TUNLITE_LAUNCH_AGENTS_DIR = ${JSON.stringify(env.agents)};`,
    `fs.appendFileSync(${JSON.stringify(startsFile)}, JSON.stringify({ pid: process.pid, kind: ${JSON.stringify(kind)}, ts: Date.now() }) + '\\n');`,
    `require(${JSON.stringify(entry)});`,
    '',
  ].join('\n'));
}

async function checkLaunchdRestart(root, label) {
  if (process.platform !== 'darwin') return { skipped: true, reason: 'not macOS' };

  const worker = path.join(root, 'worker.js');
  const starts = path.join(root, 'starts.jsonl');
  writeWorker(worker, starts, 'original');

  const ctx = {
    label,
    nodePath: process.execPath,
    entry: worker,
    logDir: root,
    launchAgentsDir: root,
    pathEnv: process.env.PATH || '',
  };

  try {
    const fresh = launchd.install(ctx);
    assert.equal(fresh.ok, true, `fresh launchd install failed: ${fresh.output || ''}`);
    const first = await waitFor(() => readStarts(starts)[0], 'fresh launchd worker did not start', 30000);

    const reinstall = launchd.install(ctx);
    assert.equal(reinstall.ok, true, `launchd reinstall failed: ${reinstall.output || ''}`);
    const second = await waitFor(() => {
      const rows = readStarts(starts);
      return rows.length >= 2 ? rows[rows.length - 1] : null;
    }, 'kickstart did not relaunch the worker', 30000);
    assert.notEqual(second.pid, first.pid, 'kickstart did not replace the running process');

    const replacement = path.join(root, 'worker-replacement.js');
    writeWorker(replacement, starts, 'replacement');
    const changed = launchd.install({ ...ctx, entry: replacement });
    assert.equal(changed.ok, true, `launchd definition reload failed: ${changed.output || ''}`);
    const third = await waitFor(() => {
      const rows = readStarts(starts);
      return rows.find((row) => row.kind === 'replacement') || null;
    }, 'launchd reinstall did not adopt the changed ProgramArguments', 35000);

    return { freshPid: first.pid, restartedPid: second.pid, reloadedPid: third.pid };
  } finally {
    launchd.uninstall(ctx);
  }
}

async function checkServicelessRelaunch(root, label) {
  const previous = {
    home: process.env.TUNLITE_HOME,
    socket: process.env.TUNLITE_SOCKET,
    label: process.env.TUNLITE_SERVICE_LABEL,
    agents: process.env.TUNLITE_LAUNCH_AGENTS_DIR,
  };
  process.env.TUNLITE_HOME = path.join(root, 'home');
  process.env.TUNLITE_SERVICE_LABEL = `${label}.serviceless`;
  process.env.TUNLITE_LAUNCH_AGENTS_DIR = path.join(root, 'agents');

  const io = { out: { write() {} }, err: { write() {} } };
  try {
    const before = await ensureDaemon(io);
    // A stale plist must not be mistaken for a loaded service. In that state
    // launchctl cannot own the restart, so the daemon must use self-relaunch.
    fs.mkdirSync(process.env.TUNLITE_LAUNCH_AGENTS_DIR, { recursive: true });
    fs.writeFileSync(
      path.join(process.env.TUNLITE_LAUNCH_AGENTS_DIR, `${process.env.TUNLITE_SERVICE_LABEL}.plist`),
      '<plist/>\n',
    );
    const result = await restartDaemonProcess(io);
    assert.equal(result.restarted, true, JSON.stringify(result));
    assert.equal(result.via, 'self-relaunch');
    assert.notEqual(result.pid, before.pid);
    return { oldPid: before.pid, newPid: result.pid };
  } finally {
    try { await ipc.request(paths.socketPath(), 'shutdown', {}); } catch (_) {}
    await waitFor(async () => !(await daemonPing()), 'isolated daemon did not stop').catch(() => {});
    if (previous.home === undefined) delete process.env.TUNLITE_HOME; else process.env.TUNLITE_HOME = previous.home;
    if (previous.label === undefined) delete process.env.TUNLITE_SERVICE_LABEL; else process.env.TUNLITE_SERVICE_LABEL = previous.label;
    if (previous.agents === undefined) delete process.env.TUNLITE_LAUNCH_AGENTS_DIR; else process.env.TUNLITE_LAUNCH_AGENTS_DIR = previous.agents;
  }
}

async function checkManagedApply(root, label) {
  if (process.platform !== 'darwin') return { skipped: true, reason: 'not macOS' };

  const previous = {
    home: process.env.TUNLITE_HOME,
    label: process.env.TUNLITE_SERVICE_LABEL,
    agents: process.env.TUNLITE_LAUNCH_AGENTS_DIR,
    context: autostart.context,
  };
  const env = {
    home: path.join(root, 'managed-home'),
    socket: path.join('/tmp', `tunlite-validation-${process.pid}.sock`),
    label,
    agents: path.join(root, 'managed-agents'),
  };
  process.env.TUNLITE_HOME = env.home;
  process.env.TUNLITE_SOCKET = env.socket;
  process.env.TUNLITE_SERVICE_LABEL = env.label;
  process.env.TUNLITE_LAUNCH_AGENTS_DIR = env.agents;

  const starts = path.join(root, 'managed-starts.jsonl');
  const originalEntry = path.join(root, 'daemon-original.js');
  const replacementEntry = path.join(root, 'daemon-replacement.js');
  writeDaemonWrapper(originalEntry, starts, 'original', env);
  writeDaemonWrapper(replacementEntry, starts, 'replacement', env);
  const ctx = {
    label,
    nodePath: process.execPath,
    entry: originalEntry,
    logDir: path.join(root, 'managed-logs'),
    launchAgentsDir: env.agents,
    pathEnv: process.env.PATH || '',
  };
  const io = { out: { write() {} }, err: { write() {} } };

  try {
    const fresh = launchd.install(ctx);
    assert.equal(fresh.ok, true, `managed launchd install failed: ${fresh.output || ''}`);
    const before = await waitFor(() => daemonPing(), 'managed daemon did not start', 15000);

    // This is the real update restart path: context now points at a different
    // anchored entry, so adapter.apply must rewrite/reload the launchd definition
    // instead of merely kickstarting the stale in-memory command.
    autostart.context = () => ({ ...ctx, entry: replacementEntry });
    const result = await restartDaemonProcess(io);
    if (!result.restarted) assert.equal(result.reason, 'service-relaunch-timeout', JSON.stringify(result));
    const after = result.restarted
      ? result
      : await waitFor(async () => {
        const ping = await daemonPing();
        return ping && ping.pid !== before.pid ? ping : null;
      }, 'managed service did not eventually relaunch', 35000);
    if (result.restarted) assert.equal(result.via, 'service');
    assert.notEqual(after.pid, before.pid);
    await waitFor(
      () => readStarts(starts).find((row) => row.kind === 'replacement'),
      'managed update did not adopt the replacement entry',
      15000,
    );
    return { oldPid: before.pid, newPid: after.pid, confirmed: result.restarted ? 'cli' : 'eventual' };
  } catch (error) {
    let direct = '';
    try { direct = JSON.stringify(await ipc.request(env.socket, 'ping', {}, { timeoutMs: 5000 })); }
    catch (ipcError) { direct = `${ipcError.code || ipcError.name}: ${ipcError.message}`; }
    const uid = process.getuid ? process.getuid() : 0;
    const printed = cp.spawnSync('launchctl', ['print', `gui/${uid}/${label}`], { encoding: 'utf8' });
    const pid = /\bpid = (\d+)/.exec(printed.stdout || '');
    const opened = pid ? cp.spawnSync('lsof', ['-n', '-P', '-p', pid[1]], { encoding: 'utf8' }) : null;
    const read = (name) => {
      try { return fs.readFileSync(path.join(ctx.logDir, name), 'utf8'); } catch (_) { return ''; }
    };
    error.message += `\nlaunchctl:\n${printed.stdout || printed.stderr || ''}`;
    error.message += `\nstdout:\n${read('launchd.out.log')}\nstderr:\n${read('launchd.err.log')}`;
    error.message += `\nstarts:\n${readStarts(starts).map(JSON.stringify).join('\n')}`;
    error.message += `\nlsof:\n${opened ? opened.stdout || opened.stderr || '' : ''}`;
    error.message += `\nsocket expected: ${env.socket} exists=${fs.existsSync(env.socket)}`;
    error.message += `\ndirect ping: ${direct}`;
    throw error;
  } finally {
    autostart.context = previous.context;
    launchd.uninstall(ctx);
    await waitFor(async () => !(await daemonPing()), 'managed daemon did not stop').catch(() => {});
    if (previous.home === undefined) delete process.env.TUNLITE_HOME; else process.env.TUNLITE_HOME = previous.home;
    if (previous.socket === undefined) delete process.env.TUNLITE_SOCKET; else process.env.TUNLITE_SOCKET = previous.socket;
    if (previous.label === undefined) delete process.env.TUNLITE_SERVICE_LABEL; else process.env.TUNLITE_SERVICE_LABEL = previous.label;
    if (previous.agents === undefined) delete process.env.TUNLITE_LAUNCH_AGENTS_DIR; else process.env.TUNLITE_LAUNCH_AGENTS_DIR = previous.agents;
  }
}

async function main() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tunlite-0.10.3-real-'));
  const label = `io.github.yuanyuanzijin.tunlite.validation.${process.pid}`;
  try {
    const managedOnly = process.argv.includes('--managed-only');
    const launchdResult = managedOnly ? { skipped: true, reason: 'managed-only' } : await checkLaunchdRestart(root, label);
    const managedResult = await checkManagedApply(root, `${label}.managed`);
    const servicelessResult = managedOnly ? { skipped: true, reason: 'managed-only' } : await checkServicelessRelaunch(root, label);
    process.stdout.write(`${JSON.stringify({ ok: true, launchd: launchdResult, managed: managedResult, serviceless: servicelessResult })}\n`);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exitCode = 1;
});
