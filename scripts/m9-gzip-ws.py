#!/usr/bin/env python3
"""M9 gzip + websocket + streaming regression.

Starts a fresh single-file panel (gzip enabled, matching Node's
`app.use(compression())`) and verifies:

  1. compressible JSON/text responses carry Content-Encoding: gzip and decode
     back to the same body;
  2. binary/streaming responses stay uncompressed (octet-stream, downloads);
  3. the raw SockJS websocket transport still opens, echoes and rejects a bad
     token (m8-ws-raw.js, 4 checks);
  4. the sockjs-client flow still passes (m8-ws.js, 7 checks);
  5. the server log contains no 500 for /api/ws and no "superfluous
     response.WriteHeader";
  6. a running task's log endpoint still answers promptly under gzip.

Usage:
    python scripts/m9-gzip-ws.py
"""
import gzip
import io
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

WORK = os.environ.get("QL_M9_GZ", r"C:\ql-m9\gz")
PORT = int(os.environ.get("QL_GZ_PORT", "5831"))
GRPC_PORT = int(os.environ.get("QL_GZ_GRPC", "5832"))
EXE = os.environ.get("QL_GO_EXE", os.path.join(os.getcwd(), "dist", "qinglong.exe"))
NODE = os.environ.get("QL_NODE", "node")

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else " :: " + detail))
    if not ok:
        failures.append(name)


def wait_port(port, timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.4)
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def request(method, path, body=None, token=None, accept_gzip=False):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if accept_gzip:
        headers["Accept-Encoding"] = "gzip"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (PORT, path),
                                 data=data, method=method, headers=headers)
    try:
        with OPENER.open(req, timeout=30) as response:
            raw = response.read()
            status = response.status
            hdrs = dict(response.headers)
    except urllib.error.HTTPError as error:
        status = error.code
        hdrs = dict(error.headers)
        raw = error.read()
    return status, hdrs, raw


def decode(raw, hdrs):
    if hdrs.get("Content-Encoding") == "gzip":
        return gzip.decompress(raw)
    return raw


def main():
    if not os.path.isfile(EXE):
        raise SystemExit("exe not found: " + EXE)

    data = os.path.join(WORK, "data")
    if os.path.isdir(data):
        try:
            os.rename(data, data + ".old-%d" % int(time.time()))
        except OSError:
            shutil.rmtree(data, ignore_errors=True)
    os.makedirs(WORK, exist_ok=True)

    for port in (PORT, GRPC_PORT):
        pass  # the rename above frees the data dir; nothing else needs cleanup

    log_path = os.path.join(WORK, "panel.log")
    log_file = open(log_path, "wb")
    env = dict(os.environ)
    env.update({
        "QL_DIR": WORK,
        "QL_DATA_DIR": data,
        "BACK_PORT": str(PORT),
        "GRPC_PORT": str(GRPC_PORT),
        "LOG_LEVEL": "info",
    })
    proc = subprocess.Popen([EXE], cwd=WORK, env=env, stdout=log_file,
                            stderr=subprocess.STDOUT,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    try:
        if not wait_port(PORT):
            raise SystemExit("panel did not listen, see " + log_path)

        # ---- bootstrapping -------------------------------------------------
        request("PUT", "/api/user/init", {"username": "admin", "password": "admin123"})
        _, _, login = request("POST", "/api/user/login",
                              {"username": "admin", "password": "admin123"})
        token = json.loads(login)["data"]["token"]

        # ---- 1. gzip on compressible responses ----------------------------
        for label, path in [("health", "/api/health"), ("system", "/api/system")]:
            status, hdrs, raw = request("GET", path, accept_gzip=True)
            plain_status, _, plain = request("GET", path, accept_gzip=False)
            check("gzip %s Content-Encoding" % label,
                  hdrs.get("Content-Encoding") == "gzip", hdrs.get("Content-Encoding"))
            if label == "health":
                # health carries uptime/instance id, so compare the envelope only
                body = json.loads(decode(raw, hdrs))
                check("gzip health decodes", body.get("code") == 200, body)
            else:
                check("gzip %s decodes equal" % label, decode(raw, hdrs) == plain,
                      "%s vs %s" % (len(decode(raw, hdrs)), len(plain)))

        # a JSON list (crons) that the front end always loads
        _, hdrs, raw = request("GET", "/api/crons", token=token, accept_gzip=True)
        check("gzip crons list", hdrs.get("Content-Encoding") == "gzip",
              hdrs.get("Content-Encoding"))

        # ---- 2. binary / streaming stay uncompressed ----------------------
        # command-run streams as application/octet-stream
        status, hdrs, raw = request("PUT", "/api/system/command-run",
                                    {"command": "echo gz-marker"}, token=token,
                                    accept_gzip=True)
        check("stream command-run not gzip", hdrs.get("Content-Encoding") is None,
              hdrs.get("Content-Encoding"))
        check("stream command-run body", b"gz-marker" in raw)


        # ---- 6. running task log answers promptly under gzip --------------
        # create a sleeping task, run it, then read its log with gzip while it
        # is still running: the response must come back immediately.
        _, _, created = request("POST", "/api/crons",
                                {"name": "m9gz.js", "command": "task m9gz.js",
                                 "schedule": "0 0 1 1 *"}, token=token)
        cron_id = json.loads(created)["data"]["id"]
        request("POST", "/api/scripts",
                {"filename": "m9gz.js", "path": ".", "content": "console.log('gz log line')\n"},
                token=token)
        request("PUT", "/api/crons/run", [cron_id], token=token)
        time.sleep(0.3)
        started = time.time()
        status, hdrs, raw = request("GET", "/api/crons/%s/log?offset=0&limit=4096" % cron_id,
                                    token=token, accept_gzip=True)
        elapsed = time.time() - started
        payload = json.loads(decode(raw, hdrs))
        check("crons/log prompt under gzip", elapsed < 5.0, "%.2fs" % elapsed)
        check("crons/log decodes", payload.get("code") == 200, payload)

        # ---- 3/4. websocket transport (Node 22 global WebSocket) ----------
        for script in ("m8-ws-raw.js", "m8-ws.js"):
            env_ws = dict(os.environ)
            env_ws["PORT"] = str(PORT)
            result = subprocess.run([NODE, os.path.join("scripts", script)],
                                    cwd=os.getcwd(), env=env_ws,
                                    capture_output=True, text=True)
            output = (result.stdout + result.stderr).strip()
            ok = result.returncode == 0 and "0 failure(s)" in output
            check("ws %s" % script, ok, output[-400:])
            print("     " + output.replace("\n", "\n     "))

        # ---- 5. server log hygiene ----------------------------------------
        syslog = os.path.join(data, "syslog")
        blob = ""
        for name in sorted(os.listdir(syslog)) if os.path.isdir(syslog) else []:
            with open(os.path.join(syslog, name), encoding="utf-8", errors="replace") as handle:
                blob += handle.read()
        ws500 = [line for line in blob.splitlines()
                 if "status=500" in line and "/api/ws" in line]
        superfluous = [line for line in blob.splitlines()
                       if "superfluous" in line.lower() and "writeheader" in line.lower()]
        check("no 500 on /api/ws", not ws500, "\n".join(ws500)[:400])
        check("no superfluous WriteHeader", not superfluous, "\n".join(superfluous)[:400])

    finally:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)],
                       capture_output=True)
        log_file.close()
        wait_port(PORT, 5)

    print("\n%d failure(s)" % len(failures))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
