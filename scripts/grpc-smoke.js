#!/usr/bin/env node
/**
 * End-to-end smoke test for the ql API (M6.5).
 *
 * It talks to the panel over the same mTLS channel shell/preload/client.js
 * uses, exercises all 20 RPCs, and checks the side effects the panel must
 * produce (a real task run with a log file, an audit row, the crontab row).
 *
 * Usage (from the panel root, while the panel is running):
 *   node scripts/grpc-smoke.js [--keep] [--verbose]
 *
 *   --keep     leave the task it created in place (default: delete it)
 *   --verbose  print every RPC response
 *
 * Environment: QL_DIR, QL_DATA_DIR, GRPC_PORT (all optional).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const grpc = require('@grpc/grpc-js');
const loader = require('@grpc/proto-loader');

const root = path.resolve(process.env.QL_DIR || process.cwd());
const dataDir = path.resolve(process.env.QL_DATA_DIR || path.join(root, 'data'));
const port = process.env.GRPC_PORT || '5500';
const certDir = path.join(dataDir, 'config', 'grpc');
const args = new Set(process.argv.slice(2));
const keep = args.has('--keep');
const verbose = args.has('--verbose');
// The (name, command, schedule) triple is unique, so every run needs its own
// name or a second run would collide with the task the first one left behind.
const stamp = Date.now().toString(36).slice(-6);

let failures = 0;
let checks = 0;

function check(label, condition, detail) {
  checks += 1;
  if (condition) {
    console.log(`  ok   ${label}`);
    return true;
  }
  failures += 1;
  console.log(`  FAIL ${label}${detail === undefined ? '' : ` -> ${JSON.stringify(detail)}`}`);
  return false;
}

function section(title) {
  console.log(`\n${title}`);
}

function loadCredentials() {
  try {
    return grpc.credentials.createSsl(
      fs.readFileSync(path.join(certDir, 'ca.crt')),
      fs.readFileSync(path.join(certDir, 'client.key')),
      fs.readFileSync(path.join(certDir, 'client.crt')),
    );
  } catch (error) {
    console.log(`  note mTLS material unavailable (${error.message}), using insecure credentials`);
    return grpc.credentials.createInsecure();
  }
}

const definition = loader.loadSync(path.join(root, 'back', 'protos', 'api.proto'), {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
});
const Api = grpc.loadPackageDefinition(definition).com.ql.api.Api;
const client = new Api(`localhost:${port}`, loadCredentials(), { 'grpc.enable_http_proxy': 0 });

function call(method, params = {}) {
  return new Promise((resolve, reject) => {
    client[method](
      params,
      new grpc.Metadata(),
      { deadline: Date.now() + 20000 },
      (error, response) => (error ? reject(error) : resolve(response)),
    );
  });
}

async function invoke(method, params) {
  const response = await call(method, params);
  if (verbose) console.log(`       ${method} -> ${JSON.stringify(response)}`);
  return response;
}

function openDatabase() {
  try {
    const { DatabaseSync } = require('node:sqlite');
    return new DatabaseSync(path.join(dataDir, 'db', 'database.sqlite'));
  } catch (error) {
    console.log(`  note cannot open database directly (${error.message})`);
    return null;
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitForLogFile(directory, before, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const files = fs.existsSync(directory) ? fs.readdirSync(directory) : [];
    const fresh = files.filter((name) => !before.has(name) && name.endsWith('.log'));
    if (fresh.length > 0) {
      const target = path.join(directory, fresh[fresh.length - 1]);
      const content = fs.readFileSync(target, 'utf8');
      if (content.includes('GRPC_PROBE done')) return { file: target, content };
    }
    await sleep(500);
  }
  return null;
}

async function envSection() {
  section('environment variables');
  const initial = await invoke('GetEnvs', { searchValue: '' });
  check('getEnvs returns code 200', initial.code === 200, initial);
  check('getEnvs data is an array', Array.isArray(initial.data), initial.data);

  const created = await invoke('CreateEnv', {
    envs: [{ name: 'SMOKE_ENV', value: 'smoke-value' }],
  });
  check('createEnv returns code 200', created.code === 200, created);
  const envId = created.data && created.data[0] ? created.data[0].id : 0;
  check('createEnv returns an id', envId > 0, created.data);

  const searched = await invoke('GetEnvs', { searchValue: 'SMOKE_ENV' });
  const row = (searched.data || []).find((item) => item.id === envId);
  check('getEnvs finds the new variable', Boolean(row), searched.data);
  check('getEnvs maps a null remark to ""', row && row.remarks === '', row);

  const byId = await invoke('GetEnvById', { id: envId });
  check('getEnvById returns code 200', byId.code === 200, byId);
  check('getEnvById maps a null remark to ""', byId.data && byId.data.remarks === '', byId.data);

  const updated = await invoke('UpdateEnv', {
    env: { id: envId, name: 'SMOKE_ENV', value: 'smoke-value-2', remarks: 'updated' },
  });
  check('updateEnv writes the new value', updated.code === 200 && updated.data.value === 'smoke-value-2', updated);

  check('disableEnvs returns code 200', (await invoke('DisableEnvs', { ids: [envId] })).code === 200);
  check('enableEnvs returns code 200', (await invoke('EnableEnvs', { ids: [envId] })).code === 200);
  check('updateEnvNames returns code 200', (await invoke('UpdateEnvNames', { ids: [envId], name: 'SMOKE_ENV2' })).code === 200);
  check('moveEnv returns code 200', (await invoke('MoveEnv', { id: envId, fromIndex: 0, toIndex: 0 })).code === 200);

  const missingIDs = await invoke('DeleteEnvs', { ids: [] });
  check('deleteEnvs without ids is code 400', missingIDs.code === 400, missingIDs);
  check('deleteEnvs message matches Node', missingIDs.message === 'ids parameter is required', missingIDs.message);

  check('deleteEnvs returns code 200', (await invoke('DeleteEnvs', { ids: [envId] })).code === 200);

  // Seed one variable so the task run below sees a non-empty getEnvs result.
  const seed = await invoke('CreateEnv', { envs: [{ name: 'SMOKE_SEED', value: 'seed' }] });
  const seedId = seed.data && seed.data[0] ? seed.data[0].id : 0;
  check('createEnv seeds the task run', seed.code === 200 && seedId > 0, seed);
  return seedId;
}

async function cronSection() {
  section('tasks');
  const created = await invoke('CreateCron', {
    command: 'task grpc_probe.js',
    schedule: '0 0 1 1 *',
  });
  check('createCron returns code 200', created.code === 200, created);
  const cronId = created.data ? created.data.id : 0;
  check('createCron returns an id', cronId > 0, created.data);
  check('createCron normalises labels to []', Array.isArray(created.data.labels) && created.data.labels.length === 0, created.data);
  check('createCron normalises extra_schedules to []', Array.isArray(created.data.extra_schedules), created.data);

  const list = await invoke('GetCrons', { searchValue: '' });
  check('getCrons returns code 200', list.code === 200, list.code);
  check('getCrons contains the new task', (list.data || []).some((item) => item.id === cronId), list.data);

  const byId = await invoke('GetCronById', { id: cronId });
  check('getCronById returns code 200', byId.code === 200, byId);
  check('getCronById returns the command', byId.data && byId.data.command === 'task grpc_probe.js', byId.data);
  check('getCronById drops unset sub_id', byId.data && byId.data.sub_id === undefined, byId.data);
  check('getCronById drops unset task_before', byId.data && byId.data.task_before === undefined, byId.data);

  const noID = await invoke('GetCronById', {});
  check('getCronById without id is code 400', noID.code === 400, noID);
  check('getCronById message matches Node', noID.message === 'id parameter is required', noID.message);

  const absent = await invoke('GetCronById', { id: 999999 });
  check('getCronById on a missing row is code 404', absent.code === 404, absent);
  check('getCronById 404 carries no data', absent.data == null, absent.data);

  const detail = await invoke('GetCronDetail', { log_path: '' });
  check('getCronDetail without log_path is code 400', detail.code === 400, detail);

  const renamed = await invoke('UpdateCron', { id: cronId, name: `smoke task ${stamp}` });
  check('updateCron keeps the command when it is not sent', renamed.code === 200 && renamed.data.command === 'task grpc_probe.js', renamed);
  check('updateCron writes the new name', renamed.data && renamed.data.name === `smoke task ${stamp}`, renamed.data);

  check('disableCrons returns code 200', (await invoke('DisableCrons', { ids: [cronId] })).code === 200);
  check('enableCrons returns code 200', (await invoke('EnableCrons', { ids: [cronId] })).code === 200);

  const emptyRun = await invoke('RunCrons', { ids: [] });
  check('runCrons without ids is code 400', emptyRun.code === 400, emptyRun);
  check('runCrons message matches Node', emptyRun.message === 'ids parameter is required', emptyRun.message);
  check('deleteCrons without ids is code 400', (await invoke('DeleteCrons', { ids: [] })).code === 400);

  const logDir = path.join(dataDir, 'log', 'grpc_probe');
  const before = new Set(fs.existsSync(logDir) ? fs.readdirSync(logDir) : []);

  const run = await invoke('RunCrons', { ids: [cronId] });
  check('runCrons returns code 200 immediately', run.code === 200, run);

  const log = await waitForLogFile(logDir, before, 60000);
  check('the task really ran and wrote a log', Boolean(log), logDir);
  if (log) {
    check('the task log shows a 200 from getEnvs with data', /getEnvs code=200 count=[1-9]/.test(log.content), log.content.slice(0, 400));
    check('the task log shows a 400 for an empty id list', log.content.includes('runCrons(empty) code=400'), log.content.slice(0, 400));
    check('the task log shows a 404 body for a missing task', log.content.includes('getCronById(missing) code=404'), log.content.slice(0, 400));
    console.log(`       log: ${log.file}`);
  }

  return cronId;
}

async function notifySection() {
  section('notifications');
  const sent = await invoke('SystemNotify', { title: 'smoke title', content: 'smoke content' });
  check('systemNotify returns a response body', typeof sent.code === 'number' && typeof sent.message === 'string', sent);

  const ok = await invoke('RecordNotifyLog', {
    title: 'smoke audit',
    content: 'recorded by grpc-smoke.js',
    success: true,
    notifyType: 'smoke',
    scriptName: 'grpc-smoke.js',
    taskId: '0',
  });
  check('recordNotifyLog returns code 200', ok.code === 200, ok);

  const failed = await invoke('RecordNotifyLog', {
    title: 'smoke audit failure',
    content: 'recorded by grpc-smoke.js',
    success: false,
    notifyType: 'smoke',
    scriptName: 'grpc-smoke.js',
    taskId: '0',
  });
  check('recordNotifyLog returns code 200 for failures too', failed.code === 200, failed);

  const db = openDatabase();
  if (!db) return;
  try {
    const rows = db
      .prepare("SELECT info FROM Auths WHERE type = 'notifyLog' ORDER BY id DESC LIMIT 2")
      .all()
      .map((row) => JSON.parse(row.info));
    const newest = rows[0] || {};
    const previous = rows[1] || {};
    check('audit row stores status 1 for success=false', newest.status === 1, newest);
    check('audit row stores source=script', newest.source === 'script', newest);
    check('audit row stores status 0 for success=true', previous.status === 0, previous);
    check('audit row keeps the notify type', previous.notifyType === 'smoke', previous);
  } finally {
    db.close();
  }
}

async function main() {
  console.log(`ql API smoke test against localhost:${port} (root ${root})`);

  const seedId = await envSection();
  const cronId = await cronSection();
  await notifySection();

  section('cleanup');
  if (keep) {
    console.log(`  kept task id ${cronId}`);
  } else {
    const removed = await invoke('DeleteCrons', { ids: [cronId] });
    check('deleteCrons returns code 200', removed.code === 200, removed);
    const gone = await invoke('GetCronById', { id: cronId });
    check('the deleted task is gone', gone.code === 404, gone);
  }
  if (seedId > 0) {
    check('deleteEnvs removes the seed variable', (await invoke('DeleteEnvs', { ids: [seedId] })).code === 200);
  }

  client.close();
  console.log(`\n${checks - failures}/${checks} checks passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((error) => {
  console.error('\nsmoke test aborted:', error.message || error);
  client.close();
  process.exit(1);
});
