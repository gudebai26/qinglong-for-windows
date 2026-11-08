// Starts the Node panel as a child process and records the child PID, so the
// contract-diff harness can stop the whole cluster tree on Windows.
//
// `node static/build/app.js` is a cluster primary that spawns its own workers.
// MSYS bash does not expose the primary PID through $!, and PowerShell/wmic are
// blocked in the sandbox, so the PID is handed over through a file and the
// harness calls `taskkill /T` on it.
const { spawn } = require('child_process');
const fs = require('fs');

const [entry, pidFile, ...args] = process.argv.slice(2);
if (!entry || !pidFile) {
  console.error('usage: node launch.js <entry.js> <pid-file> [args...]');
  process.exit(2);
}

const child = spawn(process.execPath, [entry, ...args], {
  stdio: 'inherit',
  env: process.env,
});
fs.writeFileSync(pidFile, String(child.pid));

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
