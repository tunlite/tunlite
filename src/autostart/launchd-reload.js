#!/usr/bin/env node
'use strict';

// Detached one-shot helper for replacing a loaded launchd definition. The
// installer starts this process before the managed daemon is stopped, so it
// survives even when that stop drops the SSH tunnel carrying the installer.

const cp = require('child_process');
const fs = require('fs');

const [target, domain, file, backup] = process.argv.slice(2);
if (!target || !domain || !file || !backup) process.exit(2);

const run = (args) => cp.spawnSync('launchctl', args, { stdio: 'ignore' }).status === 0;
const pause = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
// launchd applies a roughly 10-second minimum runtime/throttle to rapidly
// restarted jobs. Five seconds was too short for "install, then immediately
// update"; keep this detached helper alive long enough to outwait that window.
const attempts = 120; // 30 seconds at 250 ms

// If bootout itself fails, leave the currently loaded service alone and try the
// atomic same-definition restart as a safe fallback.
if (!run(['bootout', target])) process.exit(run(['kickstart', '-k', target]) ? 0 : 1);

for (let i = 0; i < attempts; i++) {
  if (run(['bootstrap', domain, file])) {
    try { fs.unlinkSync(backup); } catch (_) {}
    process.exit(0);
  }
  pause(250);
}

// The new definition failed to load. Restore and retry the previously working
// plist so a malformed or incompatible change does not strand the daemon.
if (fs.existsSync(backup)) {
  try { fs.copyFileSync(backup, file); } catch (_) {}
  for (let i = 0; i < attempts; i++) {
    if (run(['bootstrap', domain, file])) process.exit(1);
    pause(250);
  }
}

process.exit(1);
