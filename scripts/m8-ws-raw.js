// Direct SockJS websocket transport check (gzip-aware regression).
//
// The browser client silently falls back to xhr-streaming when the upgrade
// fails, so the regular WS test can pass while every connection costs a 500 on
// the server. This test talks the raw websocket transport (Node 22 ships a
// global WebSocket) and asserts the open frame arrives. PORT is taken from the
// environment so the gzip regression run can point it at any instance.
const http = require('http');

const BASE = `http://127.0.0.1:${process.env.PORT || 5798}`;
const failures = [];

function check(name, ok, detail) {
  console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok ? '' : ' :: ' + JSON.stringify(detail)));
  if (!ok) failures.push(name);
}

function call(method, path, payload) {
  return new Promise((resolve, reject) => {
    const body = payload === undefined ? null : JSON.stringify(payload);
    const request = http.request(
      BASE + path,
      { method, headers: { 'Content-Type': 'application/json' } },
      (response) => {
        let data = '';
        response.on('data', (chunk) => (data += chunk));
        response.on('end', () => resolve({ status: response.statusCode, body: JSON.parse(data || '{}') }));
      }
    );
    request.on('error', reject);
    if (body) request.write(body);
    request.end();
  });
}

function openTransport(token, label) {
  const url = `ws://127.0.0.1:${process.env.PORT || 5798}/api/ws/000/${label}/websocket?token=${encodeURIComponent(token)}`;
  return new Promise((resolve) => {
    const socket = new WebSocket(url);
    const state = { frames: [], closed: null, opened: false };
    const timer = setTimeout(() => resolve(state), 5000);
    socket.onmessage = (event) => {
      state.frames.push(String(event.data));
      if (String(event.data).startsWith('o')) {
        state.opened = true;
        clearTimeout(timer);
        resolve(state);
      }
    };
    socket.onclose = (event) => {
      state.closed = event.code;
      clearTimeout(timer);
      resolve(state);
    };
    socket.onerror = () => {};
    state.socket = socket;
  });
}

function waitFor(state, predicate, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const started = Date.now();
    const timer = setInterval(() => {
      const found = state.frames.find(predicate);
      if (found || Date.now() - started > timeoutMs) {
        clearInterval(timer);
        resolve(found || null);
      }
    }, 50);
  });
}

(async () => {
  await call('PUT', '/api/user/init', { username: 'admin', password: 'admin123' });
  const login = await call('POST', '/api/user/login', { username: 'admin', password: 'admin123' });
  const token = (login.body.data || {}).token;
  check('login', !!token, login.body);

  const good = await openTransport(token, 'rawws1');
  check('websocket transport opens', good.opened, { frames: good.frames, closed: good.closed });
  good.socket.send('["ws-transport-marker"]');
  const echo = await waitFor(good, (frame) => frame.includes('ws-transport-marker'));
  check('websocket transport echoes', !!echo, good.frames);
  good.socket.close();

  const bad = await openTransport('nope', 'rawws2');
  const closeFrame = await waitFor(bad, (frame) => frame.startsWith('c['), 5000);
  check('websocket transport rejects bad token', !!closeFrame && closeFrame.startsWith('c[404'), {
    opened: bad.opened,
    closed: bad.closed,
    frames: bad.frames,
  });

  console.log(failures.length === 0 ? '\n0 failure(s)' : `\n${failures.length} failure(s)`);
  process.exit(failures.length === 0 ? 0 : 1);
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
