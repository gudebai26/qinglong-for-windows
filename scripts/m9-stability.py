#!/usr/bin/env python3
"""M9 stability: repeated start/stop and a dense task burst.

1. 20 start/stop cycles: after each stop, the panel port must be free and no
   qinglong process may survive; the next start must bind immediately.
2. A dense burst (one task every 2s for a few minutes) while sampling the
   panel's RSS and heap: neither may grow linearly with task count.

Usage:
    python scripts/m9-stability.py            # full (20 restarts + burst)
    python scripts/m9-stability.py --quick    # 5 restarts + short burst
"""
import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

WORK = os.environ.get("QL_M9_STAB", r"C:\ql-m9\stab")
PORT = int(os.environ.get("QL_STAB_PORT", "5851"))
GRPC_PORT = int(os.environ.get("QL_STAB_GRPC", "5852"))
EXE = os.environ.get("QL_GO_EXE", os.path.join(os.getcwd(), "dist", "qinglong.exe"))
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else " :: " + str(detail)[:300]))
    if not ok:
        failures.append(name)


def port_free(port):
    with socket.socket() as sock:
        sock.settimeout(0.3)
        return sock.connect_ex(("127.0.0.1", port)) != 0


def wait_port(port, timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.3)
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def rss_of(pid):
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-Command",
             "(Get-Process -Id %d -ErrorAction SilentlyContinue).WorkingSet64" % pid],
            capture_output=True, text=True, timeout=10).stdout.strip()
        value = "".join(ch for ch in out if ch.isdigit())
        return int(value) if value else 0
    except Exception:
        return 0


def health(pid=None):
    try:
        with OPENER.open("http://127.0.0.1:%d/api/health" % PORT, timeout=5) as resp:
            return json.loads(resp.read())
    except Exception:
        return None


def start_panel():
    data = os.path.join(WORK, "data")
    os.makedirs(WORK, exist_ok=True)
    env = dict(os.environ)
    env.update({"QL_DIR": WORK, "QL_DATA_DIR": data, "BACK_PORT": str(PORT),
                "GRPC_PORT": str(GRPC_PORT), "LOG_LEVEL": "error"})
    return subprocess.Popen([EXE], cwd=WORK, env=env, stdout=subprocess.DEVNULL,
                            stderr=subprocess.STDOUT,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)


def stop_panel(proc):
    if proc.poll() is None:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)], capture_output=True)
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--quick", action="store_true")
    args = parser.parse_args()
    cycles = 5 if args.quick else 20
    burst_tasks = 10 if args.quick else 60

    if not os.path.isfile(EXE):
        raise SystemExit("exe not found")

    data = os.path.join(WORK, "data")
    if os.path.isdir(data):
        try:
            os.rename(data, data + ".old-%d" % int(time.time()))
        except OSError:
            shutil.rmtree(data, ignore_errors=True)
    os.makedirs(WORK, exist_ok=True)

    # ---- 1. restart cycles -------------------------------------------------
    first_boot = True
    for i in range(cycles):
        proc = start_panel()
        if not wait_port(PORT):
            check("cycle %d boot" % (i + 1), False)
            stop_panel(proc)
            break
        if first_boot:
            # seed the admin account once
            try:
                req = urllib.request.Request(
                    "http://127.0.0.1:%d/api/user/init" % PORT,
                    data=json.dumps({"username": "admin", "password": "admin123"}).encode(),
                    headers={"Content-Type": "application/json"}, method="PUT")
                OPENER.open(req, timeout=5).read()
            except Exception:
                pass
            first_boot = False
        stop_panel(proc)
        time.sleep(0.5)
        freed = port_free(PORT)
        leftover = proc.poll() is not None
        if not freed or not leftover:
            check("cycle %d clean shutdown" % (i + 1), False,
                  "freed=%s leftover=%s" % (freed, not leftover))
    print("restart cycles done")

    # ---- 2. dense burst ---------------------------------------------------
    proc = start_panel()
    if not wait_port(PORT):
        check("burst boot", False)
        return 1

    try:
        def api(method, path, payload=None, token=None):
            data = json.dumps(payload).encode() if payload is not None else None
            headers = {"Content-Type": "application/json"}
            if token:
                headers["Authorization"] = "Bearer " + token
            req = urllib.request.Request("http://127.0.0.1:%d%s" % (PORT, path),
                                         data=data, method=method, headers=headers)
            with OPENER.open(req, timeout=30) as resp:
                return resp.status, resp.read()

        _, body = api("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = json.loads(body)["data"]["token"]
        api("POST", "/api/scripts",
            {"filename": "stab.js", "path": ".", "content": "console.log('stab')\n"}, token)

        rss_samples, heap_samples = [], []
        for i in range(burst_tasks):
            api("POST", "/api/crons",
                {"name": "stab-%d.js" % i, "command": "task stab.js", "schedule": "0 0 1 1 *"},
                token)
            api("PUT", "/api/crons/run", [i + 1], token)
            time.sleep(2)
            rss_samples.append(rss_of(proc.pid))
            h = health()
            if h:
                heap_samples.append(h["data"]["metrics"]["memory"]["used"])

        def slope(values):
            if len(values) < 2:
                return 0
            return values[-1] - values[0]

        # a linear leak would grow by the same amount every task; allow noise
        rss_growth = slope(rss_samples)
        heap_growth = slope(heap_samples)
        check("RSS not growing linearly", rss_growth < 64 * 1024 * 1024,
              "first=%s last=%s (%.1f MB)" % (rss_samples[0], rss_samples[-1], rss_growth / 1e6))
        check("heap not growing linearly", heap_growth < 32 * 1024 * 1024,
              "first=%s last=%s (%.1f MB)" % (heap_samples[0], heap_samples[-1], heap_growth / 1e6))
        print("RSS samples (MB):", [round(r / 1e6, 1) for r in rss_samples])
        print("heap samples (MB):", [round(h / 1e6, 1) for h in heap_samples])
    finally:
        stop_panel(proc)

    check("port free after burst", port_free(PORT))
    print("\n%d failure(s)" % len(failures))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
