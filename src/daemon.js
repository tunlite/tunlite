'use strict';

const fs = require('fs');
const path = require('path');
const { Supervisor, STATE } = require('./supervisor');
const { Server } = require('./ipc');
const { LogHub } = require('./log');
const { Alerter } = require('./alerter');
const config = require('./config');
const paths = require('./paths');

const { VERSION } = require('./version');
const DAEMON_CH = '_daemon';

class Daemon {
  constructor(opts = {}) {
    this.configFile = opts.configFile || paths.configFile();
    this.socketPath = opts.socketPath || paths.socketPath();
    this.pidFile = opts.pidFile || paths.pidFile();
    this.sshBinary = opts.sshBinary || null;
    // Injectable so a relaunch-on-shutdown can be unit-tested without forking
    // (or actually exiting) the test process.
    this._spawn = opts.spawn || require('child_process').spawn;
    this._exit = opts.exit || process.exit;
    this.logs = new LogHub({ dir: opts.logDir || paths.logDir() });
    this.supervisors = new Map();
    this.config = config.defaultConfig();
    this.alerter = opts.alerter || new Alerter({ settings: this.config.settings, log: (l) => this.log(l) });
    this.startedAt = Date.now();
    this.server = null;
  }

  log(line) {
    this.logs.write(DAEMON_CH, line);
  }

  async start() {
    this.config = config.load(this.configFile);
    this.alerter.setSettings(this.config.settings);
    // A leftover pidfile means the previous daemon didn't shut down cleanly
    // (graceful shutdown unlinks it) — treat that as a crash of the prior run.
    let crashed = false;
    try { crashed = fs.existsSync(this.pidFile); } catch (_) {}
    this.server = new Server(this._handlers());
    await this.server.listen(this.socketPath);
    this._writePid();
    this.log(`daemon started pid=${process.pid} socket=${this.socketPath}`);
    if (crashed) this.alerter.daemonEvent('daemon-crash');
    this.alerter.daemonEvent('daemon-up');
    this.reconcile(this.config);
    this._installSignals();
    return this;
  }

  _writePid() {
    try {
      fs.mkdirSync(path.dirname(this.pidFile), { recursive: true });
      fs.writeFileSync(this.pidFile, String(process.pid));
    } catch (_) {}
  }

  _supervisorOpts() {
    return this.sshBinary ? { sshBinary: this.sshBinary } : {};
  }

  _wire(sup) {
    sup.on('log', (line) => this.logs.write(sup.tunnel.name, line));
    sup.on('state', (s) => {
      this.logs.write(sup.tunnel.name, `state -> ${s.state}`);
      this.alerter.onState(sup.tunnel.name, sup.tunnel.host, sup.status());
    });
    sup.on('needs-auth', () => this.log(`tunnel ${sup.tunnel.name} needs key setup (run: tunlite setup-key ${sup.tunnel.host})`));
  }

  // Create, wire, register and (optionally delayed) start a supervisor for a
  // tunnel definition. Shared by reconcile() and restartTunnels().
  _spawnSupervisor(name, def, delayMs = 0) {
    const sup = new Supervisor(def, this.config.settings, this._supervisorOpts());
    this._wire(sup);
    this.supervisors.set(name, sup);
    if (delayMs > 0) {
      setTimeout(() => { if (this.supervisors.get(name) === sup) sup.start(); }, delayMs);
    } else {
      sup.start();
    }
    return sup;
  }

  // Sync running supervisors against the desired (enabled) tunnels in config.
  reconcile(cfg) {
    this.config = cfg;
    const desired = new Map();
    for (const t of cfg.tunnels) if (t.enabled) desired.set(t.name, t);

    let stoppedAny = false;
    for (const [name, sup] of this.supervisors) {
      if (!desired.has(name)) {
        this.log(`stopping ${name} (removed/disabled)`);
        sup.stop();
        this.supervisors.delete(name);
        stoppedAny = true;
      } else if (defChanged(sup.tunnel, desired.get(name))) {
        this.log(`restarting ${name} (definition changed)`);
        sup.stop();
        this.supervisors.delete(name);
        stoppedAny = true;
      } else {
        // Connection-relevant fields are unchanged, so don't bounce the tunnel.
        // Still adopt the new def so metadata-only edits (e.g. tags) show up in
        // status without forcing a reconnect.
        sup.tunnel = desired.get(name);
      }
    }
    // If we just stopped something, delay fresh starts briefly so a renamed /
    // replaced tunnel doesn't race the freed remote port (ExitOnForwardFailure).
    const startDelay = stoppedAny ? 700 : 0;
    for (const [name, t] of desired) {
      if (!this.supervisors.has(name)) {
        this._spawnSupervisor(name, t, startDelay);
        this.log(`started ${name}`);
      }
    }
  }

  reload() {
    const cfg = config.load(this.configFile);
    this.alerter.setSettings(cfg.settings);
    this.reconcile(cfg);
    return { tunnels: cfg.tunnels.length, running: this.supervisors.size };
  }

  statusList(name) {
    const out = [];
    const names = name ? [name] : this.config.tunnels.map((t) => t.name);
    for (const n of names) {
      const sup = this.supervisors.get(n);
      if (sup) { out.push(sup.status()); continue; }
      const def = config.findTunnel(this.config, n);
      if (def) {
        out.push({
          name: n, host: def.host,
          port: def.port, identityFile: def.identityFile,
          sshOptions: def.sshOptions, jump: def.jump, tags: def.tags,
          enabled: def.enabled, autoSetupKey: def.autoSetupKey,
          state: def.enabled ? STATE.IDLE : 'disabled',
          pid: null, restarts: 0, uptimeMs: 0, lastError: null, lastExitCode: null,
          forwards: def.forwards,
        });
      }
    }
    return out;
  }

  restartTunnels(names) {
    const list = names && names.length ? names : [...this.supervisors.keys()];
    for (const n of list) {
      const def = config.findTunnel(this.config, n);
      if (!def) continue;
      const sup = this.supervisors.get(n);
      if (sup) { sup.stop(); this.supervisors.delete(n); }
      // brief delay so the old child releases its ports
      this._spawnSupervisor(n, def, 600);
    }
    return { restarted: list };
  }

  _handlers() {
    return {
      ping: async () => ({ pid: process.pid, version: VERSION, uptimeMs: Date.now() - this.startedAt }),
      status: async (args) => this.statusList(args.name),
      reload: async () => this.reload(),
      restart: async (args) => this.restartTunnels(args.names),
      logs: async (args, ctx) => {
        const n = args.n || 100;
        const channel = args.name || DAEMON_CH;
        for (const e of this.logs.tail(channel, n)) {
          ctx.push({ ts: e.ts, line: e.line });
        }
        if (!args.follow) {
          // signal completion of a non-following tail by closing the stream
          if (!ctx.streaming) ctx.push({ ts: Date.now(), line: '' });
          ctx.socket.end();
          return;
        }
        const onLine = ({ channel: ch, ts, line }) => {
          if (ch === channel) ctx.push({ ts, line });
        };
        this.logs.on('line', onLine);
        ctx.onClose(() => this.logs.removeListener('line', onLine));
        await new Promise(() => {}); // keep open until client disconnects
      },
      shutdown: async (args) => { setTimeout(() => this.shutdown({ relaunch: Boolean(args && args.relaunch) }), 10); return { ok: true }; },
    };
  }

  _installSignals() {
    const onSig = () => this.shutdown();
    process.on('SIGTERM', onSig);
    process.on('SIGINT', onSig);
  }

  async shutdown(opts = {}) {
    this.log(`daemon shutting down${opts.relaunch ? ' (relaunch)' : ''}`);
    // Suspend per-tunnel alerts so stopping every supervisor doesn't spray
    // `stopped` events — the single daemon-down covers the shutdown.
    this.alerter.suspended = true;
    const downSent = this.alerter.daemonEvent('daemon-down') || Promise.resolve();
    for (const sup of this.supervisors.values()) sup.stop();
    this.supervisors.clear();
    if (this.server) { try { await this.server.close(); } catch (_) {} }
    try { fs.unlinkSync(this.pidFile); } catch (_) {}
    // Give daemon-down up to ~1.5s to reach the webhook before we exit.
    await Promise.race([downSent, new Promise((r) => setTimeout(r, 1500))]);
    this.logs.close();
    // Relaunch on update without an OS service: spawn our successor (now running
    // the freshly anchored code) as a DETACHED, session-independent process, so
    // the restart is owned by the daemon — not by whoever sent `shutdown`. That
    // caller (e.g. `tunlite update` over a tunnel this very restart drops) may
    // already be dead; the successor still comes up. Ordering is safe: supervisors
    // are stopped and the server is closed (socket unlinked) above, and the new
    // daemon's listen() reclaims any stale socket on its own.
    if (opts.relaunch) {
      try {
        const child = this._spawn(process.execPath, [process.argv[1], 'daemon', 'run'], {
          detached: true, stdio: 'ignore', windowsHide: true,
        });
        child.unref();
      } catch (_) { /* best effort — a failed relaunch must not block exit */ }
    }
    this._exit(0);
  }
}

function defChanged(a, b) {
  const pick = (t) => JSON.stringify({
    host: t.host, port: t.port, identityFile: t.identityFile,
    jump: t.jump, forwards: t.forwards, sshOptions: t.sshOptions,
  });
  return pick(a) !== pick(b);
}

module.exports = { Daemon, defChanged };
