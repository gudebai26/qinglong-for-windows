#!/usr/bin/env python3
"""M9 packaging acceptance: the four deployment scenarios.

  1. fresh directory first boot  -> data/ + node_modules(1044) auto-created;
  2. migrate from Node            -> existing data/ + a protected node_modules
                                     survive untouched, tasks stay readable;
  3. upgrade from an older Go     -> a stale preload revision rewrites the hooks;
  4. single exe on a clean box    -> no node_modules/version.yaml shipped, still
                                     runs a JS task.

Usage:
    python scripts/m9-packaging.py
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

WORK = os.environ.get("QL_M9_PKG", r"C:\ql-m9\pkg")
EXE = os.environ.get("QL_GO_EXE", os.path.join(os.getcwd(), "dist", "qinglong.exe"))
NODE_DATA = os.environ.get("QL_NODE_DATA", r"C:\ql-m9\seed")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else " :: " + str(detail)[:300]))
    if not ok:
        failures.append(name)


def free_port(port):
    with socket.socket() as sock:
        sock.settimeout(0.3)
        return sock.connect_ex(("127.0.0.1", port)) != 0


def wait_port(port, timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.3)
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def start(exe_dir, data_dir, port):
    env = dict(os.environ)
    env.update({"QL_DIR": exe_dir, "QL_DATA_DIR": data_dir,
                "BACK_PORT": str(port), "GRPC_PORT": str(port + 1), "LOG_LEVEL": "error"})
    proc = subprocess.Popen([EXE], cwd=exe_dir, env=env, stdout=subprocess.DEVNULL,
                            stderr=subprocess.STDOUT,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    ok = wait_port(port)
    return proc, ok


def stop(proc, port):
    if proc.poll() is None:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)], capture_output=True)
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
    free_port(port)


def api(port, method, path, payload=None, token=None):
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (port, path),
                                 data=data, method=method, headers=headers)
    try:
        with OPENER.open(req, timeout=20) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def fresh(port):
    d = os.path.join(WORK, "fresh")
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    shutil.copy(EXE, os.path.join(d, "qinglong.exe"))
    proc, ok = start(d, os.path.join(d, "data"), port)
    if not ok:
        check("fresh boot", False)
        return
    try:
        check("fresh creates data/", os.path.isdir(os.path.join(d, "data")))
        check("fresh creates config.sh", os.path.isfile(os.path.join(d, "data", "config", "config.sh")))
        modules = 0
        for _root, _dirs, files in os.walk(os.path.join(d, "node_modules")):
            modules += len(files)
        check("fresh extracts 1044 files", modules == 1044, modules)
        _, body = api(port, "GET", "/api/health")
        check("fresh health ok", json.loads(body)["code"] == 200)
    finally:
        stop(proc, port)


def migrate(port):
    d = os.path.join(WORK, "migrate")
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    shutil.copy(EXE, os.path.join(d, "qinglong.exe"))
    # a Node data directory plus a protected node_modules the panel must keep
    shutil.copytree(NODE_DATA, os.path.join(d, "data"))
    nm = os.path.join(d, "node_modules")
    os.makedirs(os.path.join(nm, "some-user-pkg"), exist_ok=True)
    with open(os.path.join(nm, "some-user-pkg", "index.js"), "w") as handle:
        handle.write("module.exports = 'user-owned';\n")
    with open(os.path.join(nm, ".user-marker"), "w") as handle:
        handle.write("keep me\n")
    proc, ok = start(d, os.path.join(d, "data"), port)
    if not ok:
        check("migrate boot", False)
        return
    try:
        _, body = api(port, "POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = (json.loads(body).get("data") or {}).get("token")
        _, crons = api(port, "GET", "/api/crons", token=token)
        count = len(json.loads(crons).get("data", []))
        check("migrate tasks readable", count > 0, "crons=%d" % count)
        # the user's package and marker must be byte-identical
        kept = open(os.path.join(nm, "some-user-pkg", "index.js")).read()
        check("migrate keeps user node_modules", kept.strip() == "module.exports = 'user-owned';")
        check("migrate keeps marker", open(os.path.join(nm, ".user-marker")).read() == "keep me\n")
        # and the panel's own packages are added (missing only)
        check("migrate adds panel packages", os.path.isfile(
            os.path.join(nm, "@grpc", "grpc-js", "package.json")))
    finally:
        stop(proc, port)


def upgrade(port):
    d = os.path.join(WORK, "upgrade")
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    shutil.copy(EXE, os.path.join(d, "qinglong.exe"))
    # start once so the panel lands its preload hooks, then fake an old revision
    proc, ok = start(d, os.path.join(d, "data"), port)
    if not ok:
        check("upgrade boot", False)
        return
    stop(proc, port)
    preload = os.path.join(d, "shell", "preload")
    with open(os.path.join(preload, ".preload-revision"), "w") as handle:
        handle.write("0\n")
    with open(os.path.join(preload, "sitecustomize.js"), "w") as handle:
        handle.write("// old hook\n")
    proc, ok = start(d, os.path.join(d, "data"), port)
    if not ok:
        check("upgrade re-boot", False)
        return
    try:
        check("upgrade rewrites hook revision",
              open(os.path.join(preload, ".preload-revision")).read().strip() != "0")
        check("upgrade rewrites hook", "// old hook" not in
              open(os.path.join(preload, "sitecustomize.js")).read())
        # env.js is never overwritten
        check("upgrade keeps env.js", os.path.isfile(os.path.join(preload, "env.js")))
    finally:
        stop(proc, port)


def single_exe(port):
    d = os.path.join(WORK, "clean")
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    shutil.copy(EXE, os.path.join(d, "qinglong.exe"))
    proc, ok = start(d, os.path.join(d, "data"), port)
    if not ok:
        check("clean boot", False)
        return
    try:
        api(port, "PUT", "/api/user/init", {"username": "admin", "password": "admin123"})
        _, body = api(port, "POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = json.loads(body)["data"]["token"]
        api(port, "POST", "/api/scripts",
            {"filename": "clean.js", "path": ".", "content": "console.log('clean-marker')\n"}, token)
        _, created = api(port, "POST", "/api/crons",
                         {"name": "clean.js", "command": "task clean.js", "schedule": "0 0 1 1 *"}, token)
        cid = json.loads(created)["data"]["id"]
        api(port, "PUT", "/api/crons/run", [cid], token)
        content = ""
        deadline = time.time() + 60
        while time.time() < deadline:
            _, raw = api(port, "GET", "/api/crons/%s/log?offset=0&limit=65536" % cid, token=token)
            payload = json.loads(raw)
            content = payload.get("data", "")
            if "clean-marker" in content:
                break
            time.sleep(0.5)
        check("clean exe runs a JS task", "clean-marker" in content, content[-160:])
    finally:
        stop(proc, port)


def main():
    if not os.path.isfile(EXE):
        raise SystemExit("exe not found: " + EXE)
    os.makedirs(WORK, exist_ok=True)
    fresh(5861)
    migrate(5861)
    upgrade(5861)
    single_exe(5861)
    print("\n%d failure(s)" % len(failures))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
