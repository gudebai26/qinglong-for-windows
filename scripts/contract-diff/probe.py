import json, sys, urllib.request, urllib.error, base64, hmac, hashlib, struct, time

BASE = sys.argv[1]
out = []

def totp(secret, t=None):
    t = int(time.time()) if t is None else t
    key = base64.b32decode(secret + '=' * ((8 - len(secret) % 8) % 8))
    c = struct.pack('>Q', t // 30)
    h = hmac.new(key, c, hashlib.sha1).digest()
    o = h[-1] & 0x0f
    n = struct.unpack('>I', h[o:o+4])[0] & 0x7fffffff
    return str(n % 1000000).zfill(6)

def call(step, method, path, body=None, token=None, headers=None, form=None):
    url = BASE + path
    data = None
    hdrs = {}
    if body is not None:
        data = json.dumps(body).encode()
        hdrs['Content-Type'] = 'application/json'
    if form is not None:
        data, ctype = form
        hdrs['Content-Type'] = ctype
    if token:
        hdrs['Authorization'] = 'Bearer ' + token
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode('utf-8', 'replace')
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read().decode('utf-8', 'replace')
        status = e.code
    try:
        payload = json.loads(raw)
    except Exception:
        payload = raw
    out.append({'step': step, 'method': method, 'path': path, 'status': status, 'body': payload})
    return payload

def normalize(value, key=None):
    if isinstance(value, dict):
        return {k: normalize(v, k) for k, v in value.items()}
    if isinstance(value, list):
        return [normalize(v, key) for v in value]
    if isinstance(value, str):
        if value.startswith('eyJ') and value.count('.') == 2:
            return '<jwt>'
        if key == 'secret':
            return '<secret>'
        if key == 'code' and value.isdigit():
            return '<code>'
        if key == 'url' and value.startswith('otpauth://'):
            return '<otpauth>'
        return value
    if isinstance(value, int) and not isinstance(value, bool):
        if key in ('timestamp', 'lastlogon', 'expiration', 'data'):
            return '<int>'
    return value

# --- unauthenticated surface
call('no-token', 'GET', '/api/user')
call('garbage-token', 'GET', '/api/user', token='abc.def.ghi')
call('basic-auth', 'GET', '/api/user', headers={'Authorization': 'Basic abc'})
call('bearer-empty', 'GET', '/api/user', headers={'Authorization': 'Bearer'})
call('unknown-no-token', 'GET', '/api/unknown')
call('open-user-no-token', 'GET', '/open/user')
call('open-health-no-token', 'GET', '/open/health')
call('api-404-bare', 'GET', '/api')

# --- init
call('init-admin-password', 'PUT', '/api/user/init', {'username': 'tester', 'password': 'admin'})
call('init-missing-password', 'PUT', '/api/user/init', {'username': 'tester'})
call('init-ok', 'PUT', '/api/user/init', {'username': 'tester', 'password': 'test1234'})
call('init-again', 'PUT', '/api/user/init', {'username': 'tester', 'password': 'test1234'})

# --- login
call('login-wrong', 'POST', '/api/user/login', {'username': 'tester', 'password': 'nope'})
login = call('login-ok', 'POST', '/api/user/login', {'username': 'tester', 'password': 'test1234'})
token = login['data']['token']
call('me', 'GET', '/api/user', token=token)
call('unknown-with-token', 'GET', '/api/unknown', token=token)
call('login-log', 'GET', '/api/user/login-log', token=token)
call('blacklist', 'GET', '/api/user/ip-blacklist', token=token)
call('blacklist-bad', 'PUT', '/api/user/ip-blacklist', {'ip': 'not-an-ip'}, token=token)
call('blacklist-add', 'PUT', '/api/user/ip-blacklist', {'ip': '10.1.2.3'}, token=token)
call('blacklist-del', 'DELETE', '/api/user/ip-blacklist', {'ip': '10.1.2.3'}, token=token)
call('notification', 'GET', '/api/user/notification', token=token)
call('notification-empty', 'PUT', '/api/user/notification', {}, token=token)
call('2fa-init', 'GET', '/api/user/two-factor/init', token=token)
call('2fa-active-bad', 'PUT', '/api/user/two-factor/active', {'code': '000000'}, token=token)
call('2fa-active-missing', 'PUT', '/api/user/two-factor/active', {}, token=token)
call('update-admin-password', 'PUT', '/api/user', {'username': 'tester', 'password': 'admin'}, token=token)
call('open-login-admin', 'POST', '/open/user/login', {'username': 'admin', 'password': 'admin'})
call('system-whitelist', 'POST', '/api/system', {})

# --- logout
call('logout', 'POST', '/api/user/logout', token=token)
call('me-after-logout', 'GET', '/api/user', token=token)

# --- 2FA full flow
login = call('login-2', 'POST', '/api/user/login', {'username': 'tester', 'password': 'test1234'})
token = login['data']['token']
init = call('2fa-init-2', 'GET', '/api/user/two-factor/init', token=token)
secret = init['data']['secret']
code = totp(secret)
call('2fa-activate', 'PUT', '/api/user/two-factor/active', {'code': code}, token=token)
call('me-after-2fa', 'GET', '/api/user', token=token)
call('login-challenge', 'POST', '/api/user/login', {'username': 'tester', 'password': 'test1234'})
code = totp(secret)
tf = call('2fa-login', 'PUT', '/api/user/two-factor/login', {'username': 'tester', 'password': 'test1234', 'code': code})
tf_token = tf.get('data', {}).get('token')
call('2fa-login-replay', 'PUT', '/api/user/two-factor/login', {'username': 'tester', 'password': 'test1234', 'code': code})
call('2fa-deactivate', 'PUT', '/api/user/two-factor/deactivate', token=tf_token)
call('me-after-deactivate', 'GET', '/api/user', token=tf_token)

# --- throttle: three failures then a correct attempt
for i in range(3):
    call('throttle-fail-%d' % i, 'POST', '/api/user/login', {'username': 'tester', 'password': 'bad'})
call('throttle-correct', 'POST', '/api/user/login', {'username': 'tester', 'password': 'test1234'})

# --- 100/15min limiter (keep last so the lock does not disturb earlier steps)
last = None
for i in range(100):
    last = call('rate-%d' % i, 'POST', '/api/user/login', {'username': 'x', 'password': 'y'})
call('rate-over-limit', 'POST', '/api/user/login', {'username': 'x', 'password': 'y'})

print(json.dumps([{**s, 'body': normalize(s['body'])} for s in out], ensure_ascii=False, indent=1))
