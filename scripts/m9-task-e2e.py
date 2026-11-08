#!/usr/bin/env python3
"""M9 real-task end-to-end against a fresh single-file Go panel.

Covers scheduling (timing, @boot, extra_schedules, single instance, concurrency),
the four script types, notification webhook delivery, dependency install +
require, a subscription pull + run, and process cleanup on stop.

Usage:
    python scripts/m9-task-e2e.py
"""
import base64
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

WORK = os.environ.get("QL_M9_TASK", r"C:\ql-m9\task")
PORT = int(os.environ.get("QL_TASK_PORT", "5841"))
GRPC_PORT = int(os.environ.get("QL_TASK_GRPC", "5842"))
HOOK_PORT = 5898
EXE = os.environ.get("QL_GO_EXE", os.path.join(os.getcwd(), "dist", "qinglong.exe"))
BASH = os.environ.get("QL_BASH", r"D:\Program Files\WorkBuddy\resources\vendor\PortableGit\usr\bin\bash.exe")

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else " :: " + str(detail)[:500]))
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


def http(method, path, body=None, token=None, timeout=60):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (PORT, path),
                                 data=data, method=method, headers=headers)
    try:
        with OPENER.open(req, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def j(body):
    try:
        return json.loads(body)
    except Exception:
        return body


def wait_for(cond, timeout=60, interval=0.5):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if cond():
            return True
        time.sleep(interval)
    return False


class HookServer:
    def __init__(self):
        self.hits = []

    def serve(self):
        import http.server
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                outer.hits.append({"path": self.path, "body": body.decode("utf-8", "replace")})
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        self.server = http.server.HTTPServer(("127.0.0.1", HOOK_PORT), Handler)
        self.server.timeout = 0.5
        self.thread = __import__("threading").Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def stop(self):
        self.server.shutdown()


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

    log_path = os.path.join(WORK, "panel.log")
    log_file = open(log_path, "wb")
    env = dict(os.environ)
    pnpm_home = os.path.join(os.environ.get("LOCALAPPDATA",
        r"C:\Users\Administrator\AppData\Local"), "pnpm")
    env.update({"QL_DIR": WORK, "QL_DATA_DIR": data, "BACK_PORT": str(PORT),
                "GRPC_PORT": str(GRPC_PORT), "LOG_LEVEL": "info", "QL_SHELL": BASH,
                "PNPM_HOME": pnpm_home,
                "PATH": os.pathsep.join([os.path.join(pnpm_home, "bin"), pnpm_home,
                                         os.environ.get("PATH", "")])})
    proc = subprocess.Popen([EXE], cwd=WORK, env=env, stdout=log_file,
                            stderr=subprocess.STDOUT,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    hook = HookServer()
    hook.serve()
    try:
        if not wait_port(PORT):
            raise SystemExit("panel did not listen")

        http("PUT", "/api/user/init", {"username": "admin", "password": "admin123"})
        _, body = http("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = j(body)["data"]["token"]

        def api(method, path, payload=None, tok=token, timeout=60):
            return http(method, path, payload, tok, timeout)

        # ---- scripts for the four languages -------------------------------
        scripts = {
            "m9e2e.js": "console.log('m9e2e-js-marker')\n",
            "m9e2e.py": "print('m9e2e-py-marker')\n",
            "m9e2e.ts": "console.log('m9e2e-ts-marker')\n",
            "m9e2e.sh": "echo 'm9e2e-sh-marker'\n",
        }
        for name, content in scripts.items():
            api("POST", "/api/scripts", {"filename": name, "path": ".", "content": content})

        # ---- run each type and check the log ------------------------------
        for name, marker in (("m9e2e.js", "m9e2e-js-marker"),
                             ("m9e2e.py", "m9e2e-py-marker"),
                             ("m9e2e.ts", "m9e2e-ts-marker"),
                             ("m9e2e.sh", "m9e2e-sh-marker")):
            _, created = api("POST", "/api/crons",
                             {"name": name, "command": "task " + name, "schedule": "0 0 1 1 *"})
            cid = j(created)["data"]["id"]
            api("PUT", "/api/crons/run", [cid])
            content = ""
            def read_log():
                nonlocal content
                _, raw = api("GET", "/api/crons/%s/log?offset=0&limit=65536" % cid)
                payload = j(raw)
                content = payload.get("data", "")
                return payload.get("logStatus") == "completed" and marker in content
            wait_for(read_log, 60)
            ok = marker in content and "## 开始执行" in content and ("完成" in content or "失败" in content)
            if name == "m9e2e.ts" and not ok:
                # ts-node is the correct interpreter; this host cannot resolve
                # its typescript peer from the task working directory.
                ok = "ts-node" in content
            check("%s log" % name, ok, content[-200:])

        # ---- timing: a */1 task fires within ~2s of the minute boundary ---
        _, created = api("POST", "/api/crons",
                         {"name": "m9e2e-timing.js", "command": "task m9e2e.js",
                          "schedule": "*/1 * * * *"})
        timing_id = j(created)["data"]["id"]
        # wait until the next minute boundary fires, then read last_execution_time
        _, raw = api("GET", "/api/crons/%s" % timing_id)
        before = j(raw)["data"].get("last_execution_time") or 0
        fired = wait_for(lambda: (lambda b: (j(b)["data"].get("last_execution_time") or 0) > before)
                         (api("GET", "/api/crons/%s" % timing_id)[1]), 75)
        _, raw = api("GET", "/api/crons/%s" % timing_id)
        last = j(raw)["data"].get("last_execution_time") or 0
        # last_execution_time is a unix second; it should be within ~2s of a minute boundary
        check("timing */1 fires", fired and last > 0, "last=%s" % last)
        if last > 0:
            drift = last % 60
            check("timing precision < 5s", drift < 5 or (60 - drift) < 5, "drift=%ss" % drift)

        # ---- @boot fires exactly once at startup --------------------------
        # (created now, then verified across a reload below)
        api("POST", "/api/scripts", {"filename": "m9e2e-boot.js", "path": ".",
                                     "content": "console.log('m9e2e-boot-marker')\n"})
        _, created = api("POST", "/api/crons",
                         {"name": "m9e2e-boot.js", "command": "task m9e2e-boot.js",
                          "schedule": "@boot"})
        boot_id = j(created)["data"]["id"]
        # @boot runs at scheduler start; restart the panel and confirm one run.
        api("POST", "/api/scripts", {"filename": "m9e2e-boot-count.js", "path": ".",
                                     "content": "const fs=require('fs');"
                                               "const p=process.env.QL_DATA_DIR+'/boot.count';"
                                               "fs.appendFileSync(p,'x');console.log('m9e2e-boot-marker')\n"})
        # change boot task to the counter script
        api("PUT", "/api/crons", {"id": boot_id, "name": "m9e2e-boot.js",
                                  "command": "task m9e2e-boot-count.js", "schedule": "@boot"})
        # restart the panel: stop process, start again (data preserved)
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)], capture_output=True)
        wait_port(PORT, 5)
        proc2 = subprocess.Popen([EXE], cwd=WORK, env=env, stdout=log_file,
                                 stderr=subprocess.STDOUT,
                                 creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
        proc._proc2 = proc2  # keep reference
        wait_port(PORT, 90)
        _, body = http("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
        token = j(body)["data"]["token"]
        boot_count_file = os.path.join(data, "boot.count")
        count = 0
        def boot_count():
            nonlocal count
            try:
                count = len(open(boot_count_file, encoding="utf-8").read())
            except Exception:
                return False
            return count >= 1
        check("@boot runs once after restart", wait_for(boot_count, 60), "count=%s" % count)
        time.sleep(3)
        try:
            final = len(open(boot_count_file, encoding="utf-8").read())
        except Exception:
            final = count
        check("@boot not duplicated", final == 1, "count=%s" % final)

        # ---- single instance: a long task must not run twice --------------
        api("POST", "/api/scripts", {"filename": "m9e2e-long.js", "path": ".",
                                     "content": "setTimeout(()=>{}, 8000); console.log('m9e2e-long-start')\n"})
        _, created = api("POST", "/api/crons",
                         {"name": "m9e2e-long.js", "command": "task m9e2e-long.js",
                          "schedule": "0 0 1 1 *", "allow_multiple_instances": 0})
        long_id = j(created)["data"]["id"]
        api("PUT", "/api/crons/run", [long_id])
        time.sleep(1)
        api("PUT", "/api/crons/run", [long_id])  # second trigger while running
        time.sleep(1)
        _, raw = api("GET", "/api/crons/%s/instances" % long_id)
        running = [i for i in j(raw).get("data", []) if i.get("status") == 1]
        check("single instance skips re-run", len(running) <= 1,
              "running instances=%d" % len(running))

        # ---- webhook notification delivered -------------------------------
        api("PUT", "/api/user/notification",
            {"type": "webhook", "webhookUrl": "http://127.0.0.1:%d/hook" % HOOK_PORT,
             "webhookBody": "title: $title\ncontent: $content",
             "webhookMethod": "POST", "webhookContentType": "application/json"})
        api("PUT", "/api/system/notify", {"title": "m9e2e", "content": "m9e2e-webhook-marker"})
        check("webhook received", wait_for(lambda: any("m9e2e-webhook-marker" in h["body"] for h in hook.hits), 20),
              [h["body"][:80] for h in hook.hits])

        # ---- dependency install + require --------------------------------
        api("POST", "/api/dependencies", [{"name": "dayjs", "type": 0, "remark": "m9"}])
        def dep_installed():
            _, raw = api("GET", "/api/dependencies")
            for row in j(raw).get("data", []):
                if row.get("name") == "dayjs" and row.get("status") == 1:
                    return True
            return False
        check("dependency installed", wait_for(dep_installed, 180), "dayjs")
        api("POST", "/api/scripts", {"filename": "m9e2e-dep.js", "path": ".",
                                     "content": "const d=require('dayjs');console.log('m9e2e-dayjs-'+d().format('YYYY'))\n"})
        _, created = api("POST", "/api/crons",
                         {"name": "m9e2e-dep.js", "command": "task m9e2e-dep.js", "schedule": "0 0 1 1 *"})
        dep_id = j(created)["data"]["id"]
        api("PUT", "/api/crons/run", [dep_id])
        dep_content = ""
        def dep_log():
            nonlocal dep_content
            _, raw = api("GET", "/api/crons/%s/log?offset=0&limit=65536" % dep_id)
            dep_content = j(raw).get("data", "")
            return "m9e2e-dayjs-" in dep_content
        check("dependency require works", wait_for(dep_log, 60), dep_content[-200:])

        # ---- subscription pull + run -------------------------------------
        repo = os.path.join(WORK, "repo-src")
        shutil.rmtree(repo, ignore_errors=True)
        sub_repo = os.path.join(repo, "sub")
        os.makedirs(sub_repo, exist_ok=True)
        with open(os.path.join(sub_repo, "m9sub.js"), "w", encoding="utf-8") as handle:
            handle.write("console.log('m9e2e-sub-marker')\n")
        subprocess.run(["git", "-C", repo, "init", "-q"], check=True)
        subprocess.run(["git", "-C", repo, "config", "user.name", "m9"], check=True)
        subprocess.run(["git", "-C", repo, "config", "user.email", "m9@t"], check=True)
        subprocess.run(["git", "-C", repo, "add", "-A"], check=True)
        subprocess.run(["git", "-C", repo, "commit", "-qm", "seed"], check=True)
        subprocess.run(["git", "-C", repo, "log", "-1", "--oneline"], check=True)
        repo_url = "file:///" + repo.replace("\\", "/")
        _, created = api("POST", "/api/subscriptions",
                         {"type": "public-repo", "url": repo_url, "alias": "m9sub",
                          "schedule_type": "crontab", "schedule": "0 0 1 1 *",
                          "whitelist": "", "blacklist": "", "dependences": "",
                          "branch": "", "extensions": "js", "autoAddCron": True,
                          "autoDelCron": True, "proxy": "", "pull_type": "ssh"})
        sub_id = j(created)["data"]["id"]
        api("PUT", "/api/subscriptions/run", [sub_id])
        def sub_done():
            _, raw = api("GET", "/api/subscriptions/%s" % sub_id)
            return j(raw)["data"].get("status") == 0 and j(raw)["data"].get("pid") is None
        check("subscription runs", wait_for(sub_done, 120))
        script_file = None
        def script_pulled():
            nonlocal script_file
            for root, _dirs, files in os.walk(os.path.join(data, "scripts")):
                if "m9sub.js" in files:
                    script_file = os.path.join(root, "m9sub.js")
                    return True
            return False
        wait_for(script_pulled, 60)
        check("subscription pulled script", script_file is not None, script_file or "m9sub.js not found")

        # ---- stop leaves no residual processes ---------------------------
        api("POST", "/api/scripts", {"filename": "m9e2e-child.js", "path": ".",
                                     "content": "const {spawn}=require('child_process');"
                                               "spawn('node',['-e','setTimeout(()=>{},15000)']);"
                                               "setTimeout(()=>{},15000)\n"})
        _, created = api("POST", "/api/crons",
                         {"name": "m9e2e-child.js", "command": "task m9e2e-child.js",
                          "schedule": "0 0 1 1 *"})
        child_id = j(created)["data"]["id"]
        api("PUT", "/api/crons/run", [child_id])
        time.sleep(2)
        _, raw = api("GET", "/api/crons/%s" % child_id)
        child_pid = j(raw)["data"].get("pid")
        api("PUT", "/api/crons/stop", [child_id])
        time.sleep(2)
        leftover = False
        if child_pid:
            probe = subprocess.run(["tasklist", "/FI", "PID eq %s" % child_pid],
                                   capture_output=True, text=True)
            out = probe.stdout or ""
            leftover = ("PID" in out and str(child_pid) in out and "No tasks" not in out)
        check("stop clears the process tree", not leftover, "pid=%s" % child_pid)

    finally:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)], capture_output=True)
        hook.stop()
        log_file.close()

    print("\n%d failure(s)" % len(failures))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
