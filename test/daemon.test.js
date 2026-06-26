'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { Daemon, defChanged } = require('../src/daemon');
const config = require('../src/config');

const FAKE_SSH = path.join(__dirname, '..', 'fixtures', 'fake-ssh.js');

// A baseline tunnel definition. Tests clone + tweak this to probe defChanged.
function def(extra = {}) {
  return {
    name: 'web', host: 'me@host', port: 22, identityFile: null,
    jump: [], sshOptions: [], tags: [], enabled: true, autoSetupKey: true,
    forwards: [{ type: 'remote', bind: '127.0.0.1', srcPort: 9000, destHost: 'localhost', destPort: 3000 }],
    ...extra,
  };
}

// Deep clone so mutating the copy can't alias back into the original.
function clone(t) { return JSON.parse(JSON.stringify(t)); }

// -------------------------------------------------------------------------
// 1) defChanged contract — load-bearing: it decides whether a live tunnel
//    restarts on a config edit. Pin every field that must (and must not) bounce.
// -------------------------------------------------------------------------

test('defChanged: an identical definition does not trigger a restart', () => {
  assert.equal(defChanged(def(), clone(def())), false);
});

test('defChanged: connection-relevant edits all trigger a restart', () => {
  const base = def();

  // host
  assert.equal(defChanged(base, def({ host: 'me@other' })), true);
  // port
  assert.equal(defChanged(base, def({ port: 2222 })), true);
  // identityFile
  assert.equal(defChanged(base, def({ identityFile: '~/.ssh/id_ed25519' })), true);
  // jump (ProxyJump) — regression guard: a past bug wrongly excluded this, so a
  // jump-only change failed to restart a live tunnel.
  assert.equal(defChanged(base, def({ jump: ['user@bastion:2222'] })), true);
  // forwards (any change to the forward set)
  assert.equal(defChanged(base, def({
    forwards: [{ type: 'remote', bind: '127.0.0.1', srcPort: 9001, destHost: 'localhost', destPort: 3000 }],
  })), true);
  // sshOptions
  assert.equal(defChanged(base, def({ sshOptions: ['ServerAliveInterval=15'] })), true);
});

test('defChanged: a tags-only edit does NOT trigger a restart', () => {
  // tags are metadata; the reconcile loop adopts them in place without bouncing
  // the live tunnel. This exclusion is deliberate.
  assert.equal(defChanged(def({ tags: [] }), def({ tags: ['work', 'prod'] })), false);
});

test('defChanged: an enabled-only or autoSetupKey-only edit does NOT trigger a restart', () => {
  // `enabled` is handled by reconcile()'s start/stop logic, not by defChanged;
  // `autoSetupKey` is key-setup metadata. Neither is a reconnect trigger.
  assert.equal(defChanged(def({ enabled: true }), def({ enabled: false })), false);
  assert.equal(defChanged(def({ autoSetupKey: true }), def({ autoSetupKey: false })), false);
});

// -------------------------------------------------------------------------
// 2) reconcile() behavior — drive the real Daemon with a fake Alerter (so no
//    webhooks fire) and the fake ssh binary (so supervisors spawn a harmless
//    child). We never call start(), so no IPC socket is bound. Every supervisor
//    is stopped in an after-hook so no children linger.
// -------------------------------------------------------------------------

// A no-op Alerter stub that records the daemon-scope events it's asked to send.
function fakeAlerter() {
  return {
    suspended: false,
    events: [],
    setSettings() {},
    onState() {},
    daemonEvent(e) { this.events.push(e); return Promise.resolve(); },
    forget() {},
  };
}

// Build a Daemon wired to a temp HOME, the fake ssh, and a fake alerter. Returns
// { daemon, dir }. Supervisors use FAKE_SSH_MODE=stay so they reach connected
// without a real host; the caller stops them via teardown().
function mkDaemon() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'tunlite-daemon-'));
  const d = new Daemon({
    configFile: path.join(dir, 'config.json'),
    socketPath: path.join(dir, 'd.sock'),
    pidFile: path.join(dir, 'd.pid'),
    logDir: path.join(dir, 'logs'),
    sshBinary: FAKE_SSH,
    alerter: fakeAlerter(),
  });
  return { daemon: d, dir };
}

// Stop every live supervisor and remove the temp dir. reconcile() spawns real
// (fake-ssh) children; stopping them prevents leaks across the test file.
function teardown(daemon, dir) {
  for (const sup of daemon.supervisors.values()) { try { sup.stop(); } catch (_) {} }
  daemon.supervisors.clear();
  try { daemon.logs.close(); } catch (_) {}
  fs.rmSync(dir, { recursive: true, force: true });
}

function cfgWith(tunnels) {
  const c = config.defaultConfig();
  c.tunnels = tunnels;
  return c;
}

test('reconcile: a newly-enabled tunnel gets a supervisor started', (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(() => teardown(daemon, dir));

  assert.equal(daemon.supervisors.size, 0);
  daemon.reconcile(cfgWith([def({ name: 'web' })]));
  assert.equal(daemon.supervisors.size, 1);
  assert.ok(daemon.supervisors.has('web'), 'a supervisor exists for the enabled tunnel');
});

test('reconcile: a disabled tunnel is never started; a removed tunnel is stopped', (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(() => teardown(daemon, dir));

  // disabled => not desired => no supervisor
  daemon.reconcile(cfgWith([def({ name: 'off', enabled: false })]));
  assert.equal(daemon.supervisors.size, 0);

  // bring one up, then remove it from config => it gets stopped + dropped
  daemon.reconcile(cfgWith([def({ name: 'web' })]));
  assert.ok(daemon.supervisors.has('web'));
  const sup = daemon.supervisors.get('web');
  daemon.reconcile(cfgWith([])); // 'web' no longer desired
  assert.equal(daemon.supervisors.has('web'), false, 'removed tunnel is dropped');
  assert.equal(sup._stopped, true, 'its supervisor was stopped');
});

test('reconcile: disabling an active tunnel stops it', (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(() => teardown(daemon, dir));

  daemon.reconcile(cfgWith([def({ name: 'web' })]));
  const sup = daemon.supervisors.get('web');
  assert.ok(sup);
  daemon.reconcile(cfgWith([def({ name: 'web', enabled: false })]));
  assert.equal(daemon.supervisors.has('web'), false, 'disabled tunnel is no longer supervised');
  assert.equal(sup._stopped, true);
});

test('reconcile: a connection-relevant change bounces the tunnel (new supervisor)', (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(() => teardown(daemon, dir));

  daemon.reconcile(cfgWith([def({ name: 'web', host: 'me@old' })]));
  const before = daemon.supervisors.get('web');
  assert.ok(before);

  // change the host => defChanged true => old supervisor stopped, a fresh one
  // scheduled (with a start delay, but the supervisor object is replaced now).
  daemon.reconcile(cfgWith([def({ name: 'web', host: 'me@new' })]));
  const after = daemon.supervisors.get('web');
  assert.ok(after, 'a supervisor still exists for the tunnel');
  assert.notEqual(after, before, 'the tunnel was bounced: a new supervisor replaced the old');
  assert.equal(before._stopped, true, 'the old supervisor was stopped');
  assert.equal(after.tunnel.host, 'me@new', 'the new supervisor carries the new host');
});

test('reconcile: a tags-only edit adopts the new def in place without bouncing', (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(() => teardown(daemon, dir));

  daemon.reconcile(cfgWith([def({ name: 'web', tags: ['work'] })]));
  const sup = daemon.supervisors.get('web');
  assert.ok(sup);
  assert.deepEqual(sup.status().tags, ['work']);

  // edit ONLY tags: same supervisor object (not bounced), but the def is adopted
  // so status reflects the new tags. This adopt-in-place branch exists for tags.
  daemon.reconcile(cfgWith([def({ name: 'web', tags: ['staging', 'db'] })]));
  assert.equal(daemon.supervisors.get('web'), sup, 'the live supervisor was NOT replaced');
  assert.equal(sup._stopped, false, 'the live supervisor was not stopped (still running)');
  assert.deepEqual(sup.status().tags, ['staging', 'db'], 'status reflects the new tags');
});

// -------------------------------------------------------------------------
// 3) crash detection + shutdown.
//    Crash detection is deterministic via a leftover pidfile, but it needs
//    start() (which binds the IPC socket). We isolate the socket under a temp
//    HOME and tear the daemon down in an after-hook. We assert the daemon-crash
//    event reached the (fake) alerter, then shut down cleanly.
//
//    We do NOT exercise the real shutdown() path end-to-end here: shutdown()
//    calls process.exit(0), which would kill the test runner. Its alert-
//    suspension + daemon-down behavior is structurally simple and is left to
//    the alerter unit tests; re-driving it would require stubbing process.exit
//    and real timers, which is flaky. Documented as a deliberate skip below.
// -------------------------------------------------------------------------

test('start(): a leftover pidfile from a previous run surfaces a daemon-crash alert', async (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  // Simulate an unclean prior exit: the pidfile still exists at start().
  fs.writeFileSync(daemon.pidFile, '99999');

  // Close the IPC server + stop supervisors in teardown; do NOT call the real
  // shutdown() (it would process.exit and kill the runner).
  t.after(async () => {
    for (const sup of daemon.supervisors.values()) { try { sup.stop(); } catch (_) {} }
    daemon.supervisors.clear();
    if (daemon.server) { try { await daemon.server.close(); } catch (_) {} }
    try { daemon.logs.close(); } catch (_) {}
    fs.rmSync(dir, { recursive: true, force: true });
  });

  await daemon.start();
  // The fake alerter recorded the daemon-scope events in order.
  assert.ok(daemon.alerter.events.includes('daemon-crash'), 'crash detected from the stale pidfile');
  assert.ok(daemon.alerter.events.includes('daemon-up'), 'and the normal daemon-up still fires');
  // crash must be reported before up.
  assert.ok(daemon.alerter.events.indexOf('daemon-crash') < daemon.alerter.events.indexOf('daemon-up'));
});

test('start(): a clean start (no leftover pidfile) reports daemon-up only', async (t) => {
  process.env.FAKE_SSH_MODE = 'stay';
  const { daemon, dir } = mkDaemon();
  t.after(async () => {
    for (const sup of daemon.supervisors.values()) { try { sup.stop(); } catch (_) {} }
    daemon.supervisors.clear();
    if (daemon.server) { try { await daemon.server.close(); } catch (_) {} }
    try { daemon.logs.close(); } catch (_) {}
    fs.rmSync(dir, { recursive: true, force: true });
  });

  await daemon.start();
  assert.ok(!daemon.alerter.events.includes('daemon-crash'), 'no crash on a clean start');
  assert.deepEqual(daemon.alerter.events, ['daemon-up']);
  // start() wrote a fresh pidfile for this run.
  assert.ok(fs.existsSync(daemon.pidFile), 'a fresh pidfile is written on start');
});

// -------------------------------------------------------------------------
// shutdown(relaunch) — the serviceless `update` path. The daemon must spawn its
// own detached successor (new code) as it exits, so the restart is owned by the
// daemon, not by a caller whose session the restart may sever. `spawn`/`exit`
// are injected so this runs without forking or killing the test runner.
// -------------------------------------------------------------------------

// Build a Daemon with stubbed spawn+exit, no server/supervisors — just enough to
// drive shutdown() in isolation.
function mkShutdownDaemon() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'tunlite-shutdown-'));
  const spawned = [];
  const exits = [];
  const d = new Daemon({
    configFile: path.join(dir, 'config.json'),
    socketPath: path.join(dir, 'd.sock'),
    pidFile: path.join(dir, 'd.pid'),
    logDir: path.join(dir, 'logs'),
    alerter: fakeAlerter(),
    spawn: (cmd, args, opts) => { spawned.push({ cmd, args, opts }); return { unref() {} }; },
    exit: (code) => { exits.push(code); },
  });
  return { d, dir, spawned, exits };
}

test('shutdown({relaunch:true}) spawns a detached successor, then exits', async () => {
  const { d, dir, spawned, exits } = mkShutdownDaemon();
  try {
    await d.shutdown({ relaunch: true });
    assert.equal(spawned.length, 1, 'must spawn exactly one successor');
    const { args, opts } = spawned[0];
    assert.deepEqual(args.slice(-2), ['daemon', 'run'], 'successor runs the daemon');
    assert.equal(opts.detached, true, 'successor must be detached (survive a severed session)');
    assert.equal(opts.stdio, 'ignore');
    assert.deepEqual(exits, [0], 'exits cleanly after spawning');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('shutdown() without relaunch exits without spawning a successor', async () => {
  const { d, dir, spawned, exits } = mkShutdownDaemon();
  try {
    await d.shutdown();
    assert.equal(spawned.length, 0, 'a plain shutdown must NOT respawn');
    assert.deepEqual(exits, [0]);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
