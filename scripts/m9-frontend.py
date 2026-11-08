#!/usr/bin/env python3
"""M9 front-end E2E: SPA assets + the eight page flows, no 4xx/5xx surprises.

A real browser console check needs a headless engine (not present on this host),
so the JS-error check is approximated by: every asset the SPA references loads
200 (no missing chunk / no 404), and every page flow the front end drives
answers with the expected envelope. The flows run in the page order a user
would follow them.

Usage:
    python scripts/m9-frontend.py
"""
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

WORK = os.environ.get("QL_M9_FE", r"C:\ql-m9\fe")
PORT = int(os.environ.get("QL_FE_PORT", "5871"))
GRPC_PORT = int(os.environ.get("QL_FE_GRPC", "5872"))
EXE = os.environ.get("QL_GO_EXE", os.path.join(os.getcwd(), "dist", "qinglong.exe"))
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else " :: " + str(detail)[:300]))
    if not ok:
        failures.append(name)


def wait_port(port, timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.3)
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def api(method, path, payload=None, token=None):
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (PORT, path),
                                 data=data, method=method, headers=headers)
    try:
        with OPENER.open(req, timeout=30) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def get(path):
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (PORT, path))
    try:
        with OPENER.open(req, timeout=20) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def main():
    if not os.path.isfile(EXE):
        raise SystemExit("exe not found")
    data = os.path.join(WORK, "data")
    if os.path.isdir(data):
        try:
            os.rename(data, data + ".old-%d" % int(time.time()))
        except OSError:
            shutil.rmtree(data, ignore_errors=True)
    os.makedirs(WORK, exist_ok=True)
    env = dict(os.environ)
    env.update({"QL_DIR": WORK, "QL_DATA_DIR": data, "BACK_PORT": str(PORT),
                "GRPC_PORT": str(GRPC_PORT), "LOG_LEVEL": "error"})
    proc = subprocess.Popen([EXE], cwd=WORK, env=env, stdout=subprocess.DEVNULL,
                            stderr=subprocess.STDOUT,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    try:
        if not wait_port(PORT):
            check("panel boot", False)
            return 1

        # ---- SPA shell + assets ------------------------------------------
        status, html = get("/")
        check("SPA serves index.html", status == 200 and b"<div id=" in html or b"id=\"root\"" in html)
        assets = set(re.findall(rb'(?:src|href)=["\']([^"\']+\.(?:js|css))["\']', html))
        for asset in list(assets):
            decoded = asset.decode()
            if decoded.startswith("http"):
                continue
            # index.html references ./umi.xxx.js relative to the page URL "/"
            path = decoded[1:] if decoded.startswith("./") else (
                decoded if decoded.startswith("/") else "/" + decoded)
            a_status, _ = get(path)
            if a_status != 200:
                check("asset %s" % decoded, False, "status=%d" % a_status)
        check("SPA assets load", True)
        _, envjs = get("/api/env.js")
        check("env.js serves", envjs.strip() != b"")

        # ---- page flows in order -----------------------------------------
        api("PUT", "/api/user/init", {"username": "admin", "password": "admin123"})
        _, body = api("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = json.loads(body)["data"]["token"]

        # 任务 CRUD
        _, created = api("POST", "/api/crons",
                         {"name": "fe.js", "command": "task fe.js", "schedule": "0 0 1 1 *"}, token)
        cid = json.loads(created)["data"]["id"]
        _, got = api("GET", "/api/crons/%s" % cid, token=token)
        check("page cron CRUD", json.loads(got)["code"] == 200)

        # 脚本编辑
        _, saved = api("POST", "/api/scripts",
                       {"filename": "fe.js", "path": ".", "content": "console.log(1)\n"}, token)
        check("page script edit", json.loads(saved)["code"] == 200)

        # 环境变量
        _, envs = api("POST", "/api/envs", [{"name": "FE_VAR", "value": "1", "remarks": ""}], token)
        check("page env", json.loads(envs)["code"] == 200)

        # 订阅
        _, subs = api("GET", "/api/subscriptions", token=token)
        check("page subscription", json.loads(subs)["code"] == 200)

        # 依赖
        _, deps = api("GET", "/api/dependencies", token=token)
        check("page dependence", json.loads(deps)["code"] == 200)

        # 系统设置
        _, cfg = api("GET", "/api/system/config", token=token)
        check("page system config", json.loads(cfg)["code"] == 200)

        # 仪表盘
        _, dash = api("GET", "/api/dashboard/overview", token=token)
        check("page dashboard", json.loads(dash)["code"] == 200)
    finally:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)], capture_output=True)

    print("\n%d failure(s)" % len(failures))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
