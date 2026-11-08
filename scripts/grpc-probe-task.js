/**
 * Task used by scripts/grpc-smoke.js to prove the ql API works from inside a
 * real task: the preload must expose global.QLAPI and every call must come back
 * with the panel's response body.
 *
 * name: gRPC smoke probe
 * cron: 0 0 1 1 *
 */
const marker = 'GRPC_PROBE';

(async () => {
  const envs = await QLAPI.getEnvs({ searchValue: '' });
  console.log(`${marker} getEnvs code=${envs.code} count=${(envs.data || []).length}`);

  const crons = await QLAPI.getCrons({ searchValue: '' });
  console.log(`${marker} getCrons code=${crons.code} count=${(crons.data || []).length}`);

  const missing = await QLAPI.runCrons({ ids: [] });
  console.log(`${marker} runCrons(empty) code=${missing.code} message=${missing.message}`);

  // getCronById reports a missing row in the body (code 404), it does not
  // reject: back/schedule/api.ts catches the error and answers with a code.
  const absent = await QLAPI.getCronById({ id: 999999 });
  console.log(`${marker} getCronById(missing) code=${absent.code}`);

  // QLAPI.notify is the multi-channel JavaScript sender; every configured
  // channel reports its own outcome through RecordNotifyLog, so the panel's
  // audit table gets one row per channel.
  const notified = await QLAPI.notify('smoke notify', 'sent from grpc_probe.js');
  console.log(`${marker} notify resolved=${String(notified)}`);

  console.log(`${marker} done`);
})().catch((error) => {
  console.log(`${marker} failed ${error && error.message}`);
  process.exitCode = 1;
});
