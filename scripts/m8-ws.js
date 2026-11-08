// M8 websocket acceptance: SockJS auth, echo, revocation and a live broadcast.
const path = require('path');
const http = require('http');

const BASE = `http://127.0.0.1:${process.env.PORT || 5799}`;
const SockJS = require(path.join('F:/WorkBuddy AI/qinglong/qinglong/node_modules/sockjs-client'));

let token = '';
const failures = [];

function check(name, ok, detail) {
  console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok ? '' : ' :: ' + JSON.stringify(detail)));
  if (!ok) failures.push(name);
}

function call(method, pathname, payload, bearer) {
  return new Promise((resolve, reject) => {
    const body = payload === undefined ? null : JSON.stringify(payload);
    const request = http.request(
      BASE + pathname,
      {
        method,
        headers: Object.assign(
          { 'Content-Type': 'application/json' },
          bearer ? { Authorization: 'Bearer ' + bearer } : {}
        ),
      },
      (response) => {
        let data = '';
        response.on('data', (chunk) => (data += chunk));
        response.on('end', () => {
          try {
            resolve({ status: response.statusCode, body: JSON.parse(data || '{}') });
          } catch (error) {
            resolve({ status: response.statusCode, body: data });
          }
        });
      }
    );
    request.on('error', reject);
    if (body) request.write(body);
    request.end();
  });
}

function openSocket(tokenValue, timeoutMs = 4000) {
  return new Promise((resolve) => {
    const socket = new SockJS(`${BASE}/api/ws?token=${tokenValue}`);
    const state = { socket, opened: false, messages: [], closed: null };
    const timer = setTimeout(() => resolve(state), timeoutMs);
    socket.onopen = () => {
      state.opened = true;
      clearTimeout(timer);
      resolve(state);
    };
    socket.onmessage = (event) => state.messages.push(event.data);
    socket.onclose = (event) => {
      state.closed = event.code;
      clearTimeout(timer);
      resolve(state);
    };
  });
}

function waitFor(state, predicate, timeoutMs = 8000) {
  return new Promise((resolve) => {
    const started = Date.now();
    const timer = setInterval(() => {
      const found = state.messages.find(predicate);
      if (found || Date.now() - started > timeoutMs) {
        clearInterval(timer);
        resolve(found);
      }
    }, 100);
  });
}

(async () => {
  await call('PUT', '/api/user/init', { username: 'admin', password: 'admin123' });
  const login = await call('POST', '/api/user/login', { username: 'admin', password: 'admin123' });
  token = (login.body.data || {}).token;
  check('login', !!token, login.body);

  // 1. valid token connects and echoes
  const good = await openSocket(token);
  check('ws connects with query token', good.opened, good.closed);
  good.socket.send('ping-from-test');
  const echo = await waitFor(good, (message) => message === 'ping-from-test', 4000);
  check('ws echoes client frames', echo === 'ping-from-test', good.messages);

  // 2. invalid token is rejected with the SockJS 404 close
  const bad = await openSocket('not-a-token');
  const badClosed = await new Promise((resolve) => {
    const started = Date.now();
    const timer = setInterval(() => {
      if (bad.closed !== null || Date.now() - started > 6000) {
        clearInterval(timer);
        resolve(bad.closed);
      }
    }, 100);
  });
  check('ws rejects bad token', badClosed === 404, { opened: bad.opened, closed: badClosed });

  // 3. debug script output is broadcast on manuallyRunScript
  const run = await call(
    'PUT',
    '/api/scripts/run',
    { filename: 'm8_ws.sh', path: 'm8', content: 'echo "ws-broadcast-marker"\nsleep 1\n' },
    token
  );
  check('script run started', run.status === 200 && run.body.code === 200, run.body);
  const frame = await waitFor(good, (message) => message.includes('ws-broadcast-marker'), 10000);
  let decoded = null;
  try {
    decoded = JSON.parse(frame);
  } catch (error) {
    decoded = null;
  }
  check(
    'ws broadcasts task output',
    decoded && decoded.type === 'manuallyRunScript' && decoded.message.includes('ws-broadcast-marker'),
    frame
  );

  // 4. logging out revokes the token and closes the socket
  await call('POST', '/api/user/logout', {}, token);
  const closed = await new Promise((resolve) => {
    const started = Date.now();
    const timer = setInterval(() => {
      if (good.closed !== null || Date.now() - started > 6000) {
        clearInterval(timer);
        resolve(good.closed);
      }
    }, 100);
  });
  check('ws closes after logout', closed === 401, closed);

  console.log(failures.length === 0 ? '\n0 failure(s)' : `\n${failures.length} failure(s)`);
  process.exit(failures.length === 0 ? 0 : 1);
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
