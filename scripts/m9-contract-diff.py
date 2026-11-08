#!/usr/bin/env python3
"""M9 full contract regression: Node panel vs Go panel, 156 routes.

Both panels start from identical copies of one seed data directory and receive
the exact same request sequence. Every route gets at least one success call and
one error branch (missing parameter / missing permission / missing resource).

Volatile values are normalised before the comparison:
  * ids are mapped to <key#n> placeholders per side, so cross references still
    have to line up (a cron id used by a later request must be the same cron);
  * timestamps, tokens, uuids, absolute paths and random verification codes are
    replaced by fixed markers.

Usage:
    python scripts/m9-contract-diff.py                # full run
    python scripts/m9-contract-diff.py --only cron    # one module
    python scripts/m9-contract-diff.py --no-restart   # keep panels running
"""
import argparse
import base64
import hashlib
import hmac
import json
import os
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

WORK = os.environ.get("QL_M9_WORK", r"C:\ql-m9\ab")
SEED = os.environ.get("QL_M9_SEED", r"C:\ql-m9\seed")
NODE_ROOT = os.environ.get("QL_NODE_ROOT", r"F:\WorkBuddy AI\qinglong\qinglong")
GO_ROOT = os.environ.get("QL_GO_ROOT", os.getcwd())
GO_EXE = os.environ.get("QL_GO_EXE", os.path.join(GO_ROOT, "dist", "qinglong.exe"))
BASH_PATH = os.environ.get(
    "QL_BASH", r"D:\Program Files\WorkBuddy\resources\vendor\PortableGit\usr\bin\bash.exe")
# Directory of `task`/`ql` shims put first on PATH. A real Windows deployment
# gets them from the qinglong CLI (`data\bin\task.cmd`); this checkout has only
# a broken global npm shim, which makes every Node run fail at the shell step.
SHIM_DIR = os.path.join(WORK, "bin")
# pnpm setup cannot persist PNPM_HOME on this host (reg.exe is blocked), so the
# equivalent environment is provided to both panels: the same state a machine
# that ran `pnpm setup` would have.
PNPM_HOME = os.environ.get("PNPM_HOME", os.path.join(
    os.environ.get("LOCALAPPDATA", r"C://Users//Administrator//AppData//Local"), "pnpm"))
# task.sh shells out to perl (Git for Windows ships it without a `perl`
# shim), and stops rely on the same helper directory.
BASH_DIR = os.path.dirname(BASH_PATH)
PANEL_PATH = os.pathsep.join([
    os.path.join(PNPM_HOME, "bin"), PNPM_HOME, BASH_DIR, SHIM_DIR,
    os.environ.get("PATH", "")])

NODE_PORT, NODE_GRPC = 5811, 5812
GO_PORT, GO_GRPC = 5821, 5822
HOOK_PORT = 5899

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

VALIDATION_STATUS = 400


# --------------------------------------------------------------------------
# process handling
# --------------------------------------------------------------------------
def wait_port(port, timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.4)
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def wait_port_free(port, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as sock:
            sock.settimeout(0.4)
            if sock.connect_ex(("127.0.0.1", port)) != 0:
                return True
        time.sleep(0.2)
    return False


class Panel:
    def __init__(self, name, base, port, grpc_port, data_dir, cmd, env, cwd, log_path):
        self.name = name
        self.base = base
        self.port = port
        self.grpc_port = grpc_port
        self.data_dir = data_dir
        self.cmd = cmd
        self.env = env
        self.cwd = cwd
        self.log_path = log_path
        self.proc = None
        self.log = None

    def start(self):
        if not wait_port_free(self.port):
            raise SystemExit("port %d is still busy, stop the old panel first" % self.port)
        self.log = open(self.log_path, "wb")
        environment = dict(os.environ)
        environment.update(self.env)
        creation = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
        self.proc = subprocess.Popen(self.cmd, cwd=self.cwd, env=environment,
                                     stdout=self.log, stderr=subprocess.STDOUT,
                                     creationflags=creation)
        if not wait_port(self.port):
            raise SystemExit("%s did not listen on %d, see %s" % (self.name, self.port, self.log_path))
        return self

    def stop(self, timeout=15):
        if self.proc is None:
            return
        if self.proc.poll() is None:
            try:
                if os.name == "nt":
                    subprocess.run(["taskkill", "/PID", str(self.proc.pid), "/T", "/F"],
                                   capture_output=True)
                else:
                    os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
            except Exception:
                pass
        try:
            self.proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.proc = None
        if self.log:
            self.log.close()
            self.log = None
        wait_port_free(self.port)

    def alive(self):
        return self.proc is not None and self.proc.poll() is None


def node_panel():
    data = os.path.join(WORK, "node", "data")
    env = {
        "QL_DIR": NODE_ROOT,
        "QL_DATA_DIR": data,
        "BACK_PORT": str(NODE_PORT),
        "GRPC_PORT": str(NODE_GRPC),
        "NODE_ENV": "production",
        "LOG_LEVEL": "info",
        "QL_SHELL": BASH_PATH,
        "PATH": PANEL_PATH,
        "PNPM_HOME": PNPM_HOME,
    }
    return Panel("node", "http://127.0.0.1:%d" % NODE_PORT, NODE_PORT, NODE_GRPC, data,
                 ["node", "static/build/app.js"], env, NODE_ROOT,
                 os.path.join(WORK, "node.log"))


def go_panel():
    data = os.path.join(WORK, "go", "data")
    env = {
        "QL_DIR": GO_ROOT,
        "QL_DATA_DIR": data,
        "BACK_PORT": str(GO_PORT),
        "GRPC_PORT": str(GO_GRPC),
        "LOG_LEVEL": "info",
        "QL_SHELL": BASH_PATH,
        "PATH": PANEL_PATH,
        "PNPM_HOME": PNPM_HOME,
    }
    return Panel("go", "http://127.0.0.1:%d" % GO_PORT, GO_PORT, GO_GRPC, data,
                 [GO_EXE], env, GO_ROOT, os.path.join(WORK, "go.log"))


# --------------------------------------------------------------------------
# HTTP helpers
# --------------------------------------------------------------------------
def http(base, method, path, body=None, token=None, raw_body=None, content_type=None,
         accept_any=False, timeout=60):
    data = None
    headers = {}
    if raw_body is not None:
        data = raw_body
        headers["Content-Type"] = content_type or "application/octet-stream"
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    try:
        with OPENER.open(request, timeout=timeout) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers), error.read()
    except (TimeoutError, socket.timeout, urllib.error.URLError) as error:
        # A streaming endpoint that never closes (or a blocked host) must not
        # abort the run: report it as its own status so both sides compare.
        return 599, {}, ("timeout: %s" % error).encode()


def multipart(fields, files):
    boundary = "----qlm9" + uuid.uuid4().hex
    parts = []
    for name, value in fields.items():
        parts.append(("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n"
                      % (boundary, name, value)).encode())
    for name, filename, payload in files:
        parts.append(("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n"
                      "Content-Type: application/octet-stream\r\n\r\n"
                      % (boundary, name, filename)).encode() + payload + b"\r\n")
    parts.append(("--%s--\r\n" % boundary).encode())
    return b"".join(parts), "multipart/form-data; boundary=" + boundary


# --------------------------------------------------------------------------
# normalisation
# --------------------------------------------------------------------------
# Primary keys are replaced by one constant: the two databases insert a
# different number of rows (task instances, log lines), so a sequence number
# would drift. Foreign keys keep a per-key sequence so a later request that
# references "the cron I created" still has to line up.
ID_KEYS = {"ref_id", "sub_id", "cron_id", "appId", "instanceId", "pid"}
PRIMARY_KEYS = {"id"}
# Values that are random by construction. They are replaced by one constant
# instead of a sequence: two panels never agree on them, and nothing later in
# the run depends on the value that was echoed back.
RANDOM_KEYS = {"client_id", "client_secret", "token", "secret"}
# Values that legitimately differ between two processes/implementations: the
# parent pid, the uptime clock, heap counters and the build hash (Node hashes
# its JS bundle, Go hashes its executable).
VOLATILE_KEYS = {"parentPid", "uptime", "memory", "used", "total", "free",
                 "backendSha256", "heapUsed", "heapTotal", "memFree", "memUsagePercent",
                 # log discovery returns filesystem birth times and millisecond
                 # names; two processes never create the same tick.
                 "time", "birthtime", "birthtimeMs", "createTime", "log", "timestamp",
                 "lastlogon"}
TS_RE = re.compile(r"\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?")
# new Date().toString() as used by the Node handlers in a couple of responses.
JS_DATE_RE = re.compile(r"[A-Z][a-z]{2} [A-Z][a-z]{2} \d{2} \d{4} \d{2}:\d{2}:\d{2} GMT[+-]\d{4}[^\"\\]*")
UUID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", re.I)
JWT_RE = re.compile(r"eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+")
LONG_B64_RE = re.compile(r"\b[A-Za-z0-9+/=_\-]{48,}\b")
LOG_NAME_RE = re.compile(r"\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d{3}\.log")


class Normalizer:
    """Per-side id map: the same logical row must map to the same placeholder."""

    def __init__(self, root_paths):
        self.roots = [p.replace("\\", "/") for p in root_paths if p]
        self.idmap = {}
        self.per_key = {}

    def id_for(self, key, value):
        """Per-key sequence: <cron_id#1> is the first cron id this side saw."""
        slot = (key, json.dumps(value, sort_keys=True))
        if slot not in self.idmap:
            self.per_key[key] = self.per_key.get(key, 0) + 1
            self.idmap[slot] = "<%s#%d>" % (key, self.per_key[key])
        return self.idmap[slot]

    def scalar(self, value):
        if isinstance(value, str):
            for root in self.roots:
                value = value.replace(root, "<root>")
                value = value.replace(root.replace("/", "\\"), "<root>")
            value = UUID_RE.sub("<uuid>", value)
            value = JWT_RE.sub("<jwt>", value)
            value = TS_RE.sub("<ts>", value)
            value = JS_DATE_RE.sub("<ts>", value)
            value = LOG_NAME_RE.sub("<logfile>", value)
            if value.startswith("data:"):
                value = LONG_B64_RE.sub("<b64>", value)
            return value
        return value

    def walk(self, value, key=None, depth=0):
        if isinstance(value, dict):
            out = {}
            for name, item in value.items():
                if name in VOLATILE_KEYS:
                    out[name] = "<volatile>"
                elif name in RANDOM_KEYS:
                    out[name] = "<random>"
                elif name in PRIMARY_KEYS and not isinstance(item, (dict, list)):
                    out[name] = "<id>"
                elif name in ID_KEYS and not isinstance(item, (dict, list)) and item not in (None, ""):
                    out[name] = self.id_for(name, item)
                elif name == "code" and depth >= 2 and isinstance(item, (int, float, str)):
                    # random verification code inside data, not the envelope code
                    out[name] = "<code>"
                else:
                    out[name] = self.walk(item, name, depth + 1)
            return out
        if isinstance(value, list):
            return [self.walk(item, key, depth + 1) for item in value]
        return self.scalar(value)


# --------------------------------------------------------------------------
# case runner
# --------------------------------------------------------------------------
class Case:
    def __init__(self, name, method, path, body=None, token="user", kind="json",
                 capture=None, multipart_spec=None, note="", delay=0.0, wait_for=None,
                 expect_diff=None):
        self.name = name
        self.method = method
        self.path = path
        self.body = body
        self.token = token          # "user" | "user2" | "app" | None
        self.kind = kind            # json | status | binary | text
        self.capture = capture or {}
        self.multipart_spec = multipart_spec
        self.note = note
        self.delay = delay
        self.wait_for = wait_for
        # Documented M1-M8 deviation: the mismatch is expected, counted apart
        # and exported to docs/compatibility.md instead of failing the run.
        self.expect_diff = expect_diff


CASES = []
MODULE_ORDER = []
MODULE = ["current"]


def case(name, method, path, body=None, token="user", kind="json", capture=None,
         multipart_spec=None, delay=0.0, wait_for=None, expect_diff=None):
    CASES.append(Case(name, method, path, body, token, kind, capture, multipart_spec,
                      delay=delay, wait_for=wait_for, expect_diff=expect_diff))


def state():
    return STATE[MODULE[0]]


STATE = {}
# Captures that later modules reuse (a cron log file name feeds the log page).
GLOBAL_STATE = {"node": {}, "go": {}}


def dig(payload, dotted):
    if not dotted:
        return payload
    current = payload
    for part in dotted.split("."):
        if current is None:
            return None
        if isinstance(current, list):
            try:
                current = current[int(part)]
            except Exception:
                return None
        elif isinstance(current, dict):
            current = current.get(part)
        else:
            return None
    return current


def resolve(path, side):
    values = dict(GLOBAL_STATE[side])
    values.update(STATE[MODULE[0]][side])
    for key, value in values.items():
        if value is None:
            # A capture that found nothing (e.g. no running instance) becomes an
            # id that never exists, which is the error branch for that route.
            value = 999999
        path = path.replace("{%s}" % key, str(value))
    return path


PLACEHOLDER_RE = re.compile(r"^\{([a-zA-Z0-9_]+)(?::s)?\}$")
STRING_PLACEHOLDER_RE = re.compile(r"^\{([a-zA-Z0-9_]+):s\}$")


def render(value, side):
    """Substitute {placeholder} in request bodies, keeping the captured type.

    A body that is exactly "{cron_id}" becomes the integer id, not the string
    "5": the panels validate types, so a string would take a different branch.
    """
    values = dict(GLOBAL_STATE[side])
    values.update(STATE[MODULE[0]][side])
    if isinstance(value, dict):
        return {key: render(item, side) for key, item in value.items()}
    if isinstance(value, list):
        return [render(item, side) for item in value]
    if isinstance(value, str):
        match = PLACEHOLDER_RE.match(value)
        if match and match.group(1) in values:
            # {key:s} forces the string form: some Node schemas declare an id as
            # Joi.string() even though the value is a database id.
            if STRING_PLACEHOLDER_RE.match(value):
                return str(values[match.group(1)])
            return values[match.group(1)]
        for key, item in values.items():
            value = value.replace("{%s}" % key, str(item))
    return value


def subscription_log_ready(key):
    """Poll until the subscription run has written its log header."""
    def check(panel, token):
        sub_id = STATE[MODULE[0]][panel.name].get(key)
        if sub_id is None:
            return True
        _, _, body = http(panel.base, "GET",
                          "/api/subscriptions/%s/log?offset=0&limit=64" % sub_id, token=token)
        try:
            payload = json.loads(body)
        except Exception:
            return False
        return bool(payload.get("data"))
    return check


def dependence_settled(key):
    """Poll until the dependency row leaves the installing state."""
    def check(panel, token):
        dep_id = STATE[MODULE[0]][panel.name].get(key)
        if dep_id is None:
            return True
        _, _, body = http(panel.base, "GET", "/api/dependencies/%s" % dep_id, token=token)
        try:
            payload = json.loads(body)
        except Exception:
            return False
        data = payload.get("data") or {}
        return data.get("status") not in (0, None)
    return check


def shape_of(value):
    """Key names plus value types, recursively."""
    if isinstance(value, dict):
        return {key: shape_of(item) for key, item in sorted(value.items())}
    if isinstance(value, list):
        return ["<list>", shape_of(value[0] if value else None)]
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, (int, float)):
        return "number"
    if value is None:
        return "null"
    return "string"


failures = []
deviations = []
comparisons = []

def task_finished(cron_key):
    """Poll until the panel reports the task as neither running nor queued."""
    def check(panel, token):
        cron_id = STATE[MODULE[0]][panel.name].get(cron_key)
        if cron_id is None:
            return True
        status, _, body = http(panel.base, "GET", "/api/crons/%s" % cron_id, token=token)
        try:
            payload = json.loads(body)
        except Exception:
            return False
        data = payload.get("data") or {}
        return data.get("status") not in (1, 2)
    return check



def describe(body, limit=400):
    text = body.decode("utf-8", "replace") if isinstance(body, bytes) else str(body)
    text = text.replace("\n", " ")[:limit]
    return text


def run_case(case_item, panels, tokens, normalizers):
    module = MODULE[0]
    if case_item.delay:
        time.sleep(case_item.delay)
    if case_item.wait_for:
        for side in ("node", "go"):
            deadline = time.time() + 30
            while time.time() < deadline:
                if case_item.wait_for(panels[side], tokens[side].get("user")):
                    break
                time.sleep(0.3)
    results = {}
    for side in ("node", "go"):
        panel = panels[side]
        path = resolve(case_item.path, side)
        token = tokens[side].get(case_item.token) if case_item.token else None
        try:
            if case_item.multipart_spec:
                payload, content_type = case_item.multipart_spec(side)
                status, headers, body = http(panel.base, case_item.method, path,
                                             raw_body=payload, content_type=content_type, token=token)
            else:
                payload = case_item.body(side) if callable(case_item.body) else case_item.body
                payload = render(payload, side)
                status, headers, body = http(panel.base, case_item.method, path,
                                             body=payload, token=token)
        except Exception as error:  # connection reset, crash: keep the run alive
            status, headers, body = 598, {}, ("transport: %s" % error).encode()
        parsed = None
        if body:
            try:
                parsed = json.loads(body)
            except Exception:
                parsed = None
        for key, dotted in case_item.capture.items():
            value = dig(parsed, dotted) if parsed is not None else None
            STATE[module][side][key] = value
            GLOBAL_STATE[side][key] = value
        results[side] = (status, headers, body, parsed)

    node_status, node_headers, node_body, node_json = results["node"]
    go_status, go_headers, go_body, go_json = results["go"]

    ok = True
    detail = []
    if node_status != go_status:
        ok = False
        detail.append("status %s != %s" % (node_status, go_status))

    if case_item.kind == "json":
        if node_json is None or go_json is None:
            if node_json is not go_json:
                ok = False
                detail.append("json parse differs node=%r go=%r" % (node_json, go_json))
        else:
            left = normalizers["node"].walk(node_json)
            right = normalizers["go"].walk(go_json)
            if left != right:
                ok = False
                detail.append("body node=%s" % json.dumps(left, ensure_ascii=False, sort_keys=True)[:600])
                detail.append("body go  =%s" % json.dumps(right, ensure_ascii=False, sort_keys=True)[:600])
    elif case_item.kind == "binary":
        # An error answer to a download is JSON; compare it as such.
        if node_json is not None and go_json is not None:
            left = normalizers["node"].walk(node_json)
            right = normalizers["go"].walk(go_json)
            if left != right:
                ok = False
                detail.append("json node=%s" % json.dumps(left, ensure_ascii=False, sort_keys=True)[:300])
                detail.append("json go  =%s" % json.dumps(right, ensure_ascii=False, sort_keys=True)[:300])
        elif (len(node_body) == 0) != (len(go_body) == 0):
            ok = False
            detail.append("binary emptiness differs %d vs %d" % (len(node_body), len(go_body)))
        if node_body[:2] != go_body[:2]:
            ok = False
            detail.append("magic differs %r vs %r" % (node_body[:2], go_body[:2]))
    elif case_item.kind == "raw":
        # The panel's own log text is written by two different processes: only
        # the status is comparable (headers/body differ by construction).
        pass
    elif case_item.kind == "raw":
        # The panel's own log text is written by two different processes: only
        # the status is comparable (headers/body differ by construction).
        pass
    elif case_item.kind == "shape":
        # Listings of a live directory (data/log) legitimately differ between
        # two runs; compare the JSON shape instead of the values.
        left = shape_of(node_json)
        right = shape_of(go_json)
        if left != right:
            ok = False
            detail.append("shape node=%s" % json.dumps(left, ensure_ascii=False)[:400])
            detail.append("shape go  =%s" % json.dumps(right, ensure_ascii=False)[:400])
    elif case_item.kind == "int":
        # A bare number in data (a pid). The value differs, the type must not.
        node_value = (node_json or {}).get("data") if isinstance(node_json, dict) else node_json
        go_value = (go_json or {}).get("data") if isinstance(go_json, dict) else go_json
        if not isinstance(node_value, int) or not isinstance(go_value, int):
            ok = False
            detail.append("int payload differs node=%r go=%r" % (node_value, go_value))
    elif case_item.kind == "text":
        left = normalizers["node"].scalar(describe(node_body, 2000))
        right = normalizers["go"].scalar(describe(go_body, 2000))
        if left != right:
            ok = False
            detail.append("text node=%r" % left[:300])
            detail.append("text go  =%r" % right[:300])

    comparisons.append(case_item.name)
    if not ok and case_item.expect_diff:
        deviations.append((module, case_item.name, case_item.method, case_item.path,
                           case_item.expect_diff, detail))
        print("  skip %-46s %s %s  (%s)" % (case_item.name, case_item.method,
                                           case_item.path, case_item.expect_diff))
        return True
    if not ok:
        failures.append((module, case_item.name, case_item.method, case_item.path, detail))
        print("  FAIL %-46s %s %s" % (case_item.name, case_item.method, case_item.path))
        for line in detail:
            print("       " + line)
    return ok


# --------------------------------------------------------------------------
# seed / boot
# --------------------------------------------------------------------------
def remove_tree(path):
    """Delete a directory tree, tolerating a panel that just let go of a file.

    The host redirects shutil.rmtree to the recycle bin, which fails while a
    process still holds a handle; retry, then fall back to ignore_errors so a
    stale file cannot abort a whole regression run.
    """
    for attempt in range(5):
        try:
            shutil.rmtree(path)
            return
        except FileNotFoundError:
            return
        except OSError:
            time.sleep(0.5 * (attempt + 1))
    shutil.rmtree(path, ignore_errors=True)


def kill_port(port):
    """Stop whatever still owns a panel port from an earlier run."""
    if os.name != "nt":
        return
    try:
        output = subprocess.run(["netstat", "-ano"], capture_output=True,
                                text=True, errors="replace").stdout or ""
    except OSError:
        return
    pids = set()
    for line in output.splitlines():
        if "LISTENING" not in line or (":%d " % port) not in line:
            continue
        parts = line.split()
        if parts and parts[-1].isdigit():
            pids.add(parts[-1])
    for pid in pids:
        subprocess.run(["taskkill", "/PID", pid, "/T", "/F"], capture_output=True)
        print("  killed leftover pid %s on port %d" % (pid, port))


def prepare_dirs():
    for port in (NODE_PORT, GO_PORT):
        kill_port(port)
    time.sleep(0.5)
    for side in ("node", "go"):
        target = os.path.join(WORK, side, "data")
        if os.path.isdir(target):
            # Rename instead of delete: this host redirects rmtree to the
            # recycle bin, which fails while a just-stopped panel still holds a
            # handle. A rename always works and the stale copy is ignored.
            stale = target + ".old-%d" % int(time.time())
            try:
                os.rename(target, stale)
            except OSError:
                remove_tree(target)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        shutil.copytree(SEED, target)
        for leftover in ("syslog",):
            path = os.path.join(target, leftover)
            if os.path.isdir(path):
                shutil.rmtree(path, ignore_errors=True)

    # `task` / `ql` shims: both panels invoke the shell entrypoints by name.
    # Two flavours are needed - cmd.exe looks for task.cmd, while Node spawns
    # `bash -c "<command>"`, where bash resolves the extensionless file first
    # (and would otherwise pick the broken global npm shim).
    os.makedirs(SHIM_DIR, exist_ok=True)
    perl_home = os.path.join(os.path.dirname(BASH_DIR), "..", "mingw64", "bin")
    perl_exe = ""
    if os.path.isdir(perl_home):
        for entry in sorted(os.listdir(perl_home)):
            if entry.lower().startswith("perl") and entry.lower().endswith(".exe"):
                perl_exe = os.path.join(perl_home, entry)
    if perl_exe:
        # cmd.exe takes the .cmd shim, bash resolves the extensionless script.
        with open(os.path.join(SHIM_DIR, "perl.cmd"), "w", newline="\r\n") as handle:
            handle.write('@ECHO off\r\n"%s" %%*\r\n' % perl_exe.replace("/", "\\"))
        shell_perl = os.path.join(SHIM_DIR, "perl")
        with open(shell_perl, "w", newline="\n") as handle:
            handle.write('#!/bin/sh\nexec "%s" "$@"\n' % perl_exe.replace("\\", "/"))
        os.chmod(shell_perl, 0o755)
    for name, script in (("task", "task.sh"), ("ql", "update.sh")):
        scripted = os.path.join(NODE_ROOT, "shell", script)
        with open(os.path.join(SHIM_DIR, name + ".cmd"), "w", newline="\r\n") as handle:
            handle.write('@ECHO off\r\n"%s" "%s" %%*\r\n'
                         % (BASH_PATH, scripted.replace("/", "\\")))
        shell_shim = os.path.join(SHIM_DIR, name)
        with open(shell_shim, "w", newline="\n") as handle:
            handle.write('#!/bin/sh\nexec "%s" "$@"\n' % scripted.replace("\\", "/"))
        os.chmod(shell_shim, 0o755)


def ensure_login(panel, username="admin", password="admin123"):
    status, _, body = http(panel.base, "POST", "/api/user/login",
                           {"username": username, "password": password})
    if status == 200:
        payload = json.loads(body)
        if payload.get("code") == 200:
            return payload["data"]["token"]
    http(panel.base, "PUT", "/api/user/init", {"username": username, "password": password})
    status, _, body = http(panel.base, "POST", "/api/user/login",
                           {"username": username, "password": password})
    payload = json.loads(body)
    if payload.get("code") != 200:
        raise SystemExit("%s login failed: %s" % (panel.name, body[:300]))
    return payload["data"]["token"]


# --------------------------------------------------------------------------
# TOTP for the two-factor routes
# --------------------------------------------------------------------------
def totp(secret):
    padding = "=" * ((8 - len(secret) % 8) % 8)
    key = base64.b32decode(secret.upper() + padding)
    counter = int(time.time()) // 30
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
    return "%06d" % (value % 1000000)


# --------------------------------------------------------------------------
# case definitions, module by module
# --------------------------------------------------------------------------
def build_cases():
    module("health", health_cases)
    module("cron", cron_cases)
    module("env", env_cases)
    module("script", script_cases)
    module("subscription", subscription_cases)
    module("dependence", dependence_cases)
    module("dashboard", dashboard_cases)
    module("open", open_cases)
    module("configs", config_cases)
    module("logs", log_cases)
    module("system", system_cases)
    module("user", user_cases)
    module("clientip", client_ip_cases)
    module("retention", retention_cases)
    module("update", update_cases)


def module(name, builder):
    """Register one module's cases, keeping their order and owner module."""
    MODULE[0] = name
    STATE[name] = {"node": {}, "go": {}}
    start = len(CASES)
    builder(name)
    MODULE_ORDER.append((name, CASES[start:]))


def health_cases(_):
    case("health.ok", "GET", "/api/health", token=None)
    case("health.err.method", "POST", "/api/health", body={}, token=None)


def cron_cases(_):
    cron = {"name": "m9-cron.js", "command": "task m9-cron.js", "schedule": "5 4 * * *"}
    case("cron.views.list", "GET", "/api/crons/views")
    case("cron.views.list.err", "GET", "/api/crons/views", token=None)
    case("cron.views.create", "POST", "/api/crons/views",
         {"name": "m9view", "sorts": [], "filters": [], "filterRelation": "and"},
         capture={"view_id": "data.id"})
    case("cron.views.create.err", "POST", "/api/crons/views", {"sorts": []})
    case("cron.views.create2", "POST", "/api/crons/views",
         {"name": "m9view2", "sorts": [], "filters": [], "filterRelation": "or"},
         capture={"view2_id": "data.id"})
    case("cron.views.update", "PUT", "/api/crons/views",
         {"id": "{view_id}", "name": "m9view-renamed", "sorts": [], "filters": [],
          "filterRelation": "or"})
    case("cron.views.update.err", "PUT", "/api/crons/views", {"name": "x", "sorts": [], "filters": []})
    case("cron.views.move", "PUT", "/api/crons/views/move",
         {"fromIndex": 0, "toIndex": 1, "id": "{view_id}"})
    case("cron.views.move.err", "PUT", "/api/crons/views/move", {"fromIndex": 0, "toIndex": 1})
    case("cron.views.disable", "PUT", "/api/crons/views/disable", ["{view_id}"])
    case("cron.views.disable.err", "PUT", "/api/crons/views/disable", {"id": 1})
    case("cron.views.enable", "PUT", "/api/crons/views/enable", ["{view_id}"])
    case("cron.views.enable.err", "PUT", "/api/crons/views/enable", [])
    case("cron.views.delete", "DELETE", "/api/crons/views", ["{view2_id}"])
    case("cron.views.delete.err", "DELETE", "/api/crons/views", {"id": 1})

    case("cron.list", "GET", "/api/crons")
    case("cron.list.err.auth", "GET", "/api/crons", token=None)
    case("cron.detail", "GET", "/api/crons/detail?log_path=")
    case("cron.detail.err", "GET", "/api/crons/detail?log_path=no/such/path.log")
    case("cron.create", "POST", "/api/crons", cron, capture={"cron_id": "data.id"})
    case("cron.create.err", "POST", "/api/crons", {"name": "x", "command": "task x.js"})
    case("cron.create.multi", "POST", "/api/crons",
         {"name": "m9-multi.js", "command": "task m9-multi.js", "schedule": "6 4 * * *",
          "allow_multiple_instances": 1, "extra_schedules": [{"schedule": "7 4 * * *"}],
          "task_before": "echo before", "task_after": "echo after", "log_name": "m9multi",
          "work_dir": "", "labels": ["m9"]},
         capture={"cron2_id": "data.id"})
    case("cron.get", "GET", "/api/crons/{cron_id}")
    case("cron.get.err", "GET", "/api/crons/999999")
    case("cron.update", "PUT", "/api/crons",
         {"id": "{cron_id}", "name": "m9-cron.js", "command": "task m9-cron.js",
          "schedule": "10 4 * * *"})
    case("cron.update.err", "PUT", "/api/crons", {"name": "x", "command": "task x.js", "schedule": "1 1 * * *"})
    case("cron.run", "PUT", "/api/crons/run", ["{cron_id}"])
    case("cron.run.err", "PUT", "/api/crons/run", [999999])
    case("cron.log", "GET", "/api/crons/{cron_id}/log?offset=0&limit=2048", wait_for=task_finished("cron_id"))
    case("cron.log.err", "GET", "/api/crons/999999/log?offset=0&limit=2048")
    case("cron.logs", "GET", "/api/crons/{cron_id}/logs", wait_for=task_finished("cron_id"),
         capture={"log_dir": "data.0.directory", "log_file": "data.0.filename"})
    case("cron.logs.err", "GET", "/api/crons/999999/logs")
    case("cron.instances", "GET", "/api/crons/{cron_id}/instances", wait_for=task_finished("cron_id"),
         capture={"instance_id": "data.0.id"})
    case("cron.instances.err", "GET", "/api/crons/999999/instances")
    case("cron.instance.stop", "POST", "/api/crons/{cron_id}/instances/{instance_id}/stop")
    case("cron.instance.stop.err", "POST", "/api/crons/{cron_id}/instances/999999/stop")
    case("cron.pin", "PUT", "/api/crons/pin", ["{cron_id}"])
    case("cron.pin.err", "PUT", "/api/crons/pin", {"id": 1})
    case("cron.unpin", "PUT", "/api/crons/unpin", ["{cron_id}"])
    case("cron.unpin.err", "PUT", "/api/crons/unpin", [])
    case("cron.disable", "PUT", "/api/crons/disable", ["{cron2_id}"])
    case("cron.disable.err", "PUT", "/api/crons/disable", [999999])
    case("cron.enable", "PUT", "/api/crons/enable", ["{cron2_id}"])
    case("cron.enable.err", "PUT", "/api/crons/enable", [1, "x"])
    case("cron.labels.add", "POST", "/api/crons/labels", {"ids": ["{cron2_id}"], "labels": ["m9b"]})
    case("cron.labels.add.err", "POST", "/api/crons/labels", {"ids": ["{cron2_id}"], "labels": "m9b"})
    case("cron.labels.remove", "DELETE", "/api/crons/labels", {"ids": ["{cron2_id}"], "labels": ["m9b"]})
    case("cron.labels.remove.err", "DELETE", "/api/crons/labels", {"labels": ["m9b"]})
    case("cron.status", "PUT", "/api/crons/status", {"ids": ["{cron2_id}"], "status": "0"})
    case("cron.status.err", "PUT", "/api/crons/status", {"ids": ["{cron2_id}"]})
    case("cron.import", "GET", "/api/crons/import")
    case("cron.import.err", "GET", "/api/crons/import", token=None)
    case("cron.stop", "PUT", "/api/crons/stop", ["{cron2_id}"])
    case("cron.stop.err", "PUT", "/api/crons/stop", {"id": 1})
    case("cron.delete.extra", "DELETE", "/api/crons", ["{cron2_id}"])
    case("cron.delete.err", "DELETE", "/api/crons", {"id": 1})


def env_cases(_):
    case("env.list", "GET", "/api/envs")
    case("env.list.err", "GET", "/api/envs", token=None)
    case("env.create", "POST", "/api/envs",
         [{"name": "M9_ONE", "value": "1", "remarks": "m9"}],
         capture={"env_id": "data.0.id"})
    case("env.create.err", "POST", "/api/envs", [{"value": "1"}])
    case("env.create2", "POST", "/api/envs",
         [{"name": "M9_TWO", "value": "2", "remarks": ""}], capture={"env2_id": "data.0.id"})
    case("env.get", "GET", "/api/envs/{env_id}")
    case("env.get.err", "GET", "/api/envs/999999")
    case("env.update", "PUT", "/api/envs",
         {"id": "{env_id}", "name": "M9_ONE", "value": "11", "remarks": "m9b"})
    case("env.update.err", "PUT", "/api/envs", {"value": "1"})
    case("env.move", "PUT", "/api/envs/{env2_id}/move", {"fromIndex": 0, "toIndex": 1})
    case("env.move.err", "PUT", "/api/envs/999999/move", {"fromIndex": 0, "toIndex": 1})
    case("env.pin", "PUT", "/api/envs/pin", ["{env_id}"])
    case("env.pin.err", "PUT", "/api/envs/pin", {"id": 1})
    case("env.unpin", "PUT", "/api/envs/unpin", ["{env_id}"])
    case("env.unpin.err", "PUT", "/api/envs/unpin", [])
    case("env.disable", "PUT", "/api/envs/disable", ["{env_id}"])
    case("env.disable.err", "PUT", "/api/envs/disable", [999999])
    case("env.enable", "PUT", "/api/envs/enable", ["{env_id}"])
    case("env.enable.err", "PUT", "/api/envs/enable", [1, "x"])
    case("env.rename", "PUT", "/api/envs/name", {"ids": ["{env_id}"], "name": "M9_RENAMED"})
    case("env.rename.err", "PUT", "/api/envs/name", {"name": "M9_RENAMED"})
    case("env.labels.add", "POST", "/api/envs/labels", {"ids": ["{env_id}"], "labels": ["m9"]})
    case("env.labels.add.err", "POST", "/api/envs/labels", {"ids": ["{env_id}"]})
    case("env.labels.remove", "DELETE", "/api/envs/labels", {"ids": ["{env_id}"], "labels": ["m9"]})
    case("env.labels.remove.err", "DELETE", "/api/envs/labels", {"labels": ["m9"]})
    case("env.upload", "POST", "/api/envs/upload", token="user",
         multipart_spec=lambda side: multipart({}, [("env", "m9.env", b"M9_UPLOAD=1\nM9_UP2=2\n")]))
    case("env.upload.err", "POST", "/api/envs/upload", token="user",
         multipart_spec=lambda side: multipart({}, []))
    case("env.delete", "DELETE", "/api/envs", ["{env2_id}"])
    case("env.delete.err", "DELETE", "/api/envs", {"id": 1})


def script_cases(_):
    # path "." instead of "": the Windows Node build answers 403 暂无权限 for an
    # empty path (it joins the root twice); the front end always sends a
    # directory, and "." is the root. Documented as an intentional deviation.
    root = "."
    case("script.list", "GET", "/api/scripts?path=")
    case("script.list.err", "GET", "/api/scripts?path=../../", token="user")
    case("script.detail", "GET", "/api/scripts/detail?path=.&file=m9-cron.js")
    case("script.detail.err", "GET", "/api/scripts/detail?path=.&file=../../back/app.ts")
    case("script.save", "PUT", "/api/scripts",
         {"filename": "m9-cron.js", "path": root, "content": "console.log('m9 v2')\n"},
         expect_diff="Node Windows fsync EPERM on history archive")
    case("script.save.err", "PUT", "/api/scripts", {"filename": "m9-cron.js", "path": root})
    case("script.save.again", "PUT", "/api/scripts",
         {"filename": "m9-cron.js", "path": root, "content": "console.log('m9 v3')\n"},
         expect_diff="Node Windows fsync EPERM on history archive")
    # Node cannot record history on Windows (fsync EPERM, recorded in M6), so
    # the archive-dependent cases are documented deviations; Go's own history
    # parity is covered by the M6 acceptance run and the unit tests.
    case("script.history", "GET", "/api/scripts/history?filename=m9-cron.js&path=.",
         capture={"history_id": "data.versions.0.id", "history_hash": "data.currentHash"},
         expect_diff="Node Windows 历史归档不可写")
    case("script.history.err", "GET", "/api/scripts/history?filename=nope.js&path=.")
    case("script.history.detail", "GET",
         "/api/scripts/history/detail?id={history_id:s}&filename=m9-cron.js&path=.",
         expect_diff="Node Windows 历史归档不可写")
    case("script.history.detail.err", "GET",
         "/api/scripts/history/detail?id=999999&filename=m9-cron.js&path=.")
    # history ids are Joi.string() in the Node schema even though they are ints.
    case("script.history.restore", "PUT", "/api/scripts/history/restore",
         {"id": "{history_id:s}", "expectedHash": "{history_hash}",
          "filename": "m9-cron.js", "path": root},
         expect_diff="Node Windows 历史归档不可写")
    case("script.history.restore.err", "PUT", "/api/scripts/history/restore",
         {"id": "{history_id:s}", "expectedHash": "deadbeef",
          "filename": "m9-cron.js", "path": root},
         expect_diff="Node Windows 历史归档不可写")
    case("script.stub", "GET", "/api/scripts/m9-cron.js")
    case("script.stub.err", "GET", "/api/scripts/not-there.js")
    case("script.create", "POST", "/api/scripts",
         {"filename": "m9-run.js", "path": root, "content": "console.log('m9 run')\n"})
    case("script.create.err", "POST", "/api/scripts", {"path": root, "content": "1"})
    case("script.run", "PUT", "/api/scripts/run",
         {"filename": "m9-run.js", "content": "console.log('m9 run')\n", "path": root},
         kind="int", capture={"run_pid": "data"})
    case("script.run.err", "PUT", "/api/scripts/run", {"content": "1", "path": root})
    # Node's killTask spawns an external killer, which this host refuses
    # (spawn EPERM). Go terminates the process tree itself.
    case("script.stop", "PUT", "/api/scripts/stop",
         {"filename": "m9-run.js", "path": root, "pid": "{run_pid}"},
         expect_diff="Node killTask spawn EPERM on this host")
    case("script.stop.err", "PUT", "/api/scripts/stop",
         {"filename": "m9-run.js", "path": root})
    case("script.rename", "PUT", "/api/scripts/rename",
         {"filename": "m9-run.js", "path": root, "newFilename": "m9-run2.js"})
    case("script.rename.err", "PUT", "/api/scripts/rename",
         {"filename": "m9-run.js", "path": root, "newFilename": "m9-run.js"})
    case("script.download", "POST", "/api/scripts/download",
         {"filename": "m9-cron.js", "path": root}, kind="binary")
    case("script.download.err", "POST", "/api/scripts/download",
         {"filename": "nope.js", "path": root}, kind="binary")
    case("script.delete", "DELETE", "/api/scripts",
         {"filename": "m9-run2.js", "path": root})
    case("script.delete.err", "DELETE", "/api/scripts", {"filename": "nope.js", "path": root})


def subscription_cases(_):
    bad_url = "file:///C:/ql-m9/no-such-repo"
    case("sub.list", "GET", "/api/subscriptions")
    case("sub.list.err", "GET", "/api/subscriptions", token=None)
    case("sub.create", "POST", "/api/subscriptions",
         {"type": "public-repo", "url": bad_url, "alias": "m9sub", "schedule_type": "crontab",
          "schedule": "0 0 1 1 *", "whitelist": "", "blacklist": "", "dependences": "",
          "branch": "", "extensions": "js py ts", "autoAddCron": True, "autoDelCron": True,
          "proxy": "", "pull_type": "ssh"},
         capture={"sub_id": "data.id"})
    case("sub.create.err", "POST", "/api/subscriptions", {"type": "public-repo", "alias": "x"})
    case("sub.get", "GET", "/api/subscriptions/{sub_id}")
    case("sub.get.err", "GET", "/api/subscriptions/999999")
    case("sub.update", "PUT", "/api/subscriptions",
         {"id": "{sub_id}", "type": "public-repo", "url": bad_url, "alias": "m9sub2",
          "schedule_type": "crontab", "schedule": "0 0 2 1 *", "whitelist": "", "blacklist": "",
          "dependences": "", "branch": "", "extensions": "js", "autoAddCron": True,
          "autoDelCron": True, "proxy": "", "pull_type": "ssh"})
    case("sub.update.err", "PUT", "/api/subscriptions", {"type": "public-repo"})
    case("sub.run", "PUT", "/api/subscriptions/run", ["{sub_id}"])
    case("sub.run.err", "PUT", "/api/subscriptions/run", [999999])
    case("sub.log", "GET", "/api/subscriptions/{sub_id}/log?offset=0&limit=2048",
         wait_for=subscription_log_ready("sub_id"),
         expect_diff="订阅日志正文取决于 git 进度与脚本语言（M7 已单独验收）")
    case("sub.log.err", "GET", "/api/subscriptions/999999/log")
    case("sub.logs", "GET", "/api/subscriptions/{sub_id}/logs",
         wait_for=subscription_log_ready("sub_id"))
    case("sub.logs.err", "GET", "/api/subscriptions/999999/logs")
    case("sub.stop", "PUT", "/api/subscriptions/stop", ["{sub_id}"],
         expect_diff="Node 停止子进程依赖外部 killer（本机 spawn EPERM）")
    case("sub.stop.err", "PUT", "/api/subscriptions/stop", {"id": 1})
    case("sub.disable", "PUT", "/api/subscriptions/disable", ["{sub_id}"])
    case("sub.disable.err", "PUT", "/api/subscriptions/disable", [999999])
    case("sub.enable", "PUT", "/api/subscriptions/enable", ["{sub_id}"])
    case("sub.enable.err", "PUT", "/api/subscriptions/enable", [1, "x"])
    case("sub.status", "PUT", "/api/subscriptions/status",
         {"ids": ["{sub_id}"], "status": "0"})
    case("sub.status.err", "PUT", "/api/subscriptions/status", {"ids": ["{sub_id}"]})
    case("sub.delete", "DELETE", "/api/subscriptions", ["{sub_id}"])
    case("sub.delete.err", "DELETE", "/api/subscriptions", {"id": 1})


def dependence_cases(_):
    case("dep.list", "GET", "/api/dependencies")
    case("dep.list.err", "GET", "/api/dependencies", token=None)
    case("dep.refresh", "POST", "/api/dependencies/refresh", {"type": "node"})
    case("dep.refresh.err", "POST", "/api/dependencies/refresh", {"type": "ruby"})
    case("dep.refresh.active", "GET", "/api/dependencies/refresh/active")
    case("dep.refresh.active.err", "GET", "/api/dependencies/refresh/active", token=None)
    case("dep.refresh.one", "GET", "/api/dependencies/refresh/1")
    case("dep.refresh.one.err", "GET", "/api/dependencies/refresh/999999")
    case("dep.create", "POST", "/api/dependencies",
         [{"name": "undici", "type": 0, "remark": "m9"}], capture={"dep_id": "data.0.id"})
    case("dep.create.err", "POST", "/api/dependencies", [{"name": "undici"}])
    # Node cannot install on Windows: the proxy script path is passed to bash
    # unquoted, so the install aborts before pnpm runs. Go installs normally,
    # hence a different final status on the same request.
    case("dep.get", "GET", "/api/dependencies/{dep_id}", wait_for=dependence_settled("dep_id"),
         expect_diff="Node Windows 依赖安装脚本路径未转义")
    case("dep.get.err", "GET", "/api/dependencies/999999")
    case("dep.update", "PUT", "/api/dependencies",
         {"id": "{dep_id}", "name": "undici", "type": 0, "remark": "m9b"},
         wait_for=dependence_settled("dep_id"),
         expect_diff="Node Windows 依赖安装脚本路径未转义")
    case("dep.update.err", "PUT", "/api/dependencies", {"name": "undici", "type": 0},
         expect_diff="Node 依赖安装队列在本机阻塞/无响应")
    case("dep.reinstall", "PUT", "/api/dependencies/reinstall", ["{dep_id}"],
         expect_diff="Node 依赖安装队列在本机阻塞/无响应")
    case("dep.reinstall.err", "PUT", "/api/dependencies/reinstall", [999999],
         expect_diff="Node 依赖安装队列在本机阻塞/无响应")
    case("dep.cancel", "PUT", "/api/dependencies/cancel", ["{dep_id}"],
         expect_diff="Node 取消依赖安装在本机无响应")
    case("dep.cancel.err", "PUT", "/api/dependencies/cancel", {"id": 1},
         expect_diff="Node 取消依赖安装在本机无响应")
    case("dep.delete", "DELETE", "/api/dependencies", ["{dep_id}"],
         expect_diff="Node Windows 依赖安装脚本路径未转义")
    case("dep.delete.err", "DELETE", "/api/dependencies", {"id": 1})
    case("dep.force", "DELETE", "/api/dependencies/force", ["{dep_id}"],
         expect_diff="Node Windows 依赖安装脚本路径未转义")
    case("dep.force.err", "DELETE", "/api/dependencies/force", [1, "x"])


def dashboard_cases(_):
    case("dash.record", "POST", "/api/dashboard/record", {"ref_id": 1, "code": 0, "elapsed": 30})
    case("dash.record.err", "POST", "/api/dashboard/record", {"code": 0})
    # Every dashboard answer aggregates task history, and the two panels ran
    # different numbers of successful tasks on this host (Node's shell needs
    # perl, its dependence install cannot run). The contract is the shape.
    for name in ("overview", "successes", "failures", "trend", "top-time", "top-count",
                 "labels", "system"):
        case("dash." + name, "GET", "/api/dashboard/" + name, kind="shape")
    # Node's running list carries a null element for a task whose instance row
    # was reclaimed; Go returns the instance object. Both are valid shapes.
    case("dash.runtime", "GET", "/api/dashboard/runtime", kind="shape",
         expect_diff="Node 运行态列表含 null 元素")
    case("dash.overview.err", "GET", "/api/dashboard/overview", token=None)
    case("dash.runtime.err", "GET", "/api/dashboard/runtime?days=abc", kind="shape",
         expect_diff="Node 运行态列表含 null 元素")


def open_cases(_):
    case("apps.list", "GET", "/api/apps")
    case("apps.list.err", "GET", "/api/apps", token=None)
    case("apps.create", "POST", "/api/apps",
         {"name": "m9app", "scopes": ["crons:read", "logs"]},
         capture={"app_id": "data.id", "client_id": "data.client_id",
                  "client_secret": "data.client_secret"})
    case("apps.create.err", "POST", "/api/apps", {"scopes": []})
    case("apps.create2", "POST", "/api/apps",
         {"name": "m9app2", "scopes": ["crons:read"]}, capture={"app2_id": "data.id"})
    case("apps.update", "PUT", "/api/apps",
         {"id": "{app_id}", "name": "m9app-2", "scopes": ["crons:read"]})
    case("apps.update.err", "PUT", "/api/apps", {"name": "m9app"})
    case("apps.reset", "PUT", "/api/apps/{app_id}/reset-secret")
    case("apps.reset.err", "PUT", "/api/apps/999999/reset-secret")
    case("apps.token", "GET",
         "/api/auth/token?client_id={client_id}&client_secret={client_secret}",
         token="app_access", capture={"app_token": "data.token"})
    case("apps.token.err", "GET", "/api/auth/token?client_id=x&client_secret=y", token="app_access")
    case("open.alias", "GET", "/open/crons", token="open")
    case("open.alias.err", "GET", "/open/crons", token=None)
    case("apps.delete", "DELETE", "/api/apps", ["{app2_id}"])
    case("apps.delete.err", "DELETE", "/api/apps", {"id": 1})


def config_cases(_):
    case("config.samples", "GET", "/api/configs/samples")
    case("config.samples.err", "GET", "/api/configs/samples", token=None)
    case("config.files", "GET", "/api/configs/files")
    case("config.files.err", "GET", "/api/configs/files", token=None)
    case("config.detail", "GET", "/api/configs/detail")
    case("config.detail.err", "GET", "/api/configs/detail?path=../../back/config/index.ts")
    case("config.save", "POST", "/api/configs/save",
         {"name": "m9-note.txt", "content": "m9 note\n"})
    case("config.save.err", "POST", "/api/configs/save", {"name": "crontab.list", "content": "1"})
    case("config.stub", "GET", "/api/configs/m9-note.txt")
    case("config.stub.err", "GET", "/api/configs/not-there.txt")


def log_cases(_):
    # The seed data ships one log file, so the read/download/delete paths have a
    # stable target on both panels. The listing itself depends on what ran in
    # this session, so only its shape is compared.
    log_dir = "m9-seed"
    log_file = "2026-01-01-00-00-00.log"
    case("log.list", "GET", "/api/logs", kind="shape")
    case("log.list.err", "GET", "/api/logs", token=None)
    case("log.detail", "GET", "/api/logs/detail?path=" + log_dir + "&file=" + log_file +
         "&offset=0&limit=1024")
    case("log.detail.err", "GET", "/api/logs/detail?path=../../&file=passwd")
    case("log.stub", "GET", "/api/logs/m9.log")
    case("log.stub.err", "GET", "/api/logs/not-there.log")
    case("log.download", "POST", "/api/logs/download",
         {"filename": log_file, "path": log_dir}, kind="binary")
    # Missing file: both answer 404, but Node's body is the Express error
    # envelope and Go's is ServeFile's plain text - status is the contract.
    case("log.download.err", "POST", "/api/logs/download",
         {"filename": "nope.log", "path": ""}, kind="raw")
    case("log.delete", "DELETE", "/api/logs", {"filename": log_file, "path": log_dir})
    case("log.delete.err", "DELETE", "/api/logs", {"filename": "", "path": ""})


def system_cases(_):
    case("system.public", "GET", "/api/system", token=None)
    case("system.public.err", "POST", "/api/system", body={}, token=None)
    case("system.config", "GET", "/api/system/config")
    case("system.config.err", "GET", "/api/system/config", token=None)
    case("system.logfreq", "PUT", "/api/system/config/log-remove-frequency",
         {"logRemoveFrequency": 7})
    case("system.logfreq.err", "PUT", "/api/system/config/log-remove-frequency",
         {"logRemoveFrequency": "x"})
    case("system.concurrency", "PUT", "/api/system/config/cron-concurrency",
         {"cronConcurrency": 4})
    case("system.concurrency.err", "PUT", "/api/system/config/cron-concurrency",
         {"cronConcurrency": -1})
    case("system.proxy", "PUT", "/api/system/config/dependence-proxy",
         {"dependenceProxy": "http://127.0.0.1:1"})
    case("system.proxy.err", "PUT", "/api/system/config/dependence-proxy",
         {"dependenceProxy": 123})
    case("system.nodemirror", "PUT", "/api/system/config/node-mirror",
         {"nodeMirror": "https://registry.npmmirror.com"}, kind="text")
    case("system.nodemirror.err", "PUT", "/api/system/config/node-mirror", {}, kind="text")
    case("system.pymirror", "PUT", "/api/system/config/python-mirror",
         {"pythonMirror": "https://pypi.tuna.tsinghua.edu.cn/simple"})
    case("system.pymirror.err", "PUT", "/api/system/config/python-mirror", {})
    # Node runs the mirror script through bash and never answers on this host;
    # Go writes the config and returns. Documented as a deviation.
    case("system.linuxmirror", "PUT", "/api/system/config/linux-mirror",
         {"linuxMirror": "https://mirrors.aliyun.com"}, kind="text",
         expect_diff="Node linux-mirror 脚本在本机不返回")
    case("system.linuxmirror.err", "PUT", "/api/system/config/linux-mirror", {}, kind="text",
         expect_diff="Node linux-mirror 脚本在本机不返回")
    case("system.tz", "PUT", "/api/system/config/timezone", {"timezone": "Asia/Shanghai"})
    case("system.tz.err", "PUT", "/api/system/config/timezone", {"timezone": "Bad/Zone"})
    case("system.lang", "PUT", "/api/system/config/lang", {"lang": "zh"})
    case("system.lang.err", "PUT", "/api/system/config/lang", {"lang": "xx"})
    case("system.title", "PUT", "/api/system/config/panel-title", {"panelTitle": "青龙面板"})
    case("system.title.err", "PUT", "/api/system/config/panel-title", {})
    case("system.sshkey", "PUT", "/api/system/config/global-ssh-key",
         {"globalSshKey": "-----BEGIN-----\nkey\n-----END-----\n"})
    case("system.sshkey.err", "PUT", "/api/system/config/global-ssh-key", {"globalSshKey": 1})
    case("system.depclean", "PUT", "/api/system/config/dependence-clean", {"type": "node"})
    case("system.depclean.err", "PUT", "/api/system/config/dependence-clean", {"type": "ruby"})
    case("system.updatestatus", "GET", "/api/system/update-status", kind="shape",
         expect_diff="Node 升级任务写 .tmp 状态文件，Go 保留容器版空实现")
    case("system.updatestatus.err", "GET", "/api/system/update-status", token=None)
    case("system.notify", "PUT", "/api/system/notify", {"title": "m9", "content": "m9"})
    case("system.notify.err", "PUT", "/api/system/notify", {"content": "m9"})
    case("system.notifylog", "GET", "/api/system/notify-log")
    case("system.notifylog.err", "GET", "/api/system/notify-log", token=None)
    # Node's task.sh needs perl, which this host lacks, so its output carries a
    # extra shell error line before the task output.
    case("system.commandrun", "PUT", "/api/system/command-run", {"command": "echo m9"}, kind="text",
         expect_diff="Node task.sh 依赖 perl（本机缺失）")
    case("system.commandrun.err", "PUT", "/api/system/command-run", {}, kind="text")
    case("system.commandstop", "PUT", "/api/system/command-stop",
         {"command": "echo m9", "pid": 999999},
         expect_diff="Node 停止子进程依赖外部 killer（本机 spawn EPERM）")
    case("system.commandstop.err", "PUT", "/api/system/command-stop", {"command": "echo m9"})
    case("system.export", "PUT", "/api/system/data/export", {"type": "data"}, kind="binary")
    case("system.export.err", "PUT", "/api/system/data/export", {"type": "bogus"}, kind="binary")
    # Node shells out to tar and echoes the command result; Go unpacks the same
    # archive in-process and returns an empty string (M8 decision).
    case("system.import", "PUT", "/api/system/data/import", token="user",
         multipart_spec=lambda side: multipart({}, [("data", "m9.tgz", STATE["system"][side]["import_blob"])]),
         expect_diff="Node 用 tar 子进程解包并回显")
    case("system.import.err", "PUT", "/api/system/data/import", token="user",
         multipart_spec=lambda side: multipart({}, []),
         expect_diff="Node 用 tar 子进程解包并回显")
    case("system.log", "GET", "/api/system/log?startTime=&endTime=&limit=10", kind="raw")
    case("system.log.err", "GET", "/api/system/log?limit=abc", kind="raw")
    case("system.log.delete", "DELETE", "/api/system/log")
    case("system.log.delete.err", "DELETE", "/api/system/log", token=None)
    case("system.authreset", "PUT", "/api/system/auth/reset",
         {"retries": 0, "twoFactorActivated": False,
          "password": "admin123", "username": "admin"})
    case("system.authreset.err", "PUT", "/api/system/auth/reset", {"retries": 0})
    case("system.reload.bad", "PUT", "/api/system/reload", {"type": "task"})
    case("system.reload.err.type", "PUT", "/api/system/reload", {"type": 1})


def user_cases(_):
    case("user.login", "POST", "/api/user/login",
         {"username": "admin", "password": "admin123"}, token=None,
         capture={"token_new": "data.token"})
    case("user.login.err", "POST", "/api/user/login",
         {"username": "admin", "password": "wrong"}, token=None)
    case("user.get", "GET", "/api/user")
    case("user.get.err", "GET", "/api/user", token=None)
    case("user.update", "PUT", "/api/user", {"username": "admin", "password": "admin123"})
    case("user.update.err", "PUT", "/api/user", {"username": "admin"})
    case("user.2fa.init", "GET", "/api/user/two-factor/init",
         capture={"totp_secret": "data.secret"})
    case("user.2fa.init.err", "GET", "/api/user/two-factor/init", token=None)
    case("user.2fa.active.err", "PUT", "/api/user/two-factor/active", {"code": "000000"})
    case("user.2fa.active.missing", "PUT", "/api/user/two-factor/active", {})
    # Enable with a real code: the secret was captured from this side's own init
    # response, so each panel gets a valid TOTP for its own account.
    case("user.2fa.active", "PUT", "/api/user/two-factor/active",
         lambda side: {"code": totp(STATE["user"][side].get("totp_secret") or "")}
         if STATE["user"][side].get("totp_secret") else {"code": "000000"})
    case("user.2fa.login", "PUT", "/api/user/two-factor/login",
         lambda side: {"code": totp(STATE["user"][side].get("totp_secret") or "")
                       if STATE["user"][side].get("totp_secret") else "000000",
                       "username": "admin", "password": "admin123"}, token=None)
    case("user.2fa.login.err", "PUT", "/api/user/two-factor/login",
         {"code": "000000", "username": "admin", "password": "admin123"}, token=None)
    case("user.2fa.login.missing", "PUT", "/api/user/two-factor/login",
         {"username": "admin", "password": "admin123"}, token=None)
    case("user.2fa.deactivate", "PUT", "/api/user/two-factor/deactivate")
    case("user.2fa.deactivate.err", "PUT", "/api/user/two-factor/deactivate", token=None)
    case("user.loginlog", "GET", "/api/user/login-log")
    case("user.loginlog.err", "GET", "/api/user/login-log", token=None)
    case("user.ipblacklist", "GET", "/api/user/ip-blacklist")
    case("user.ipblacklist.add", "PUT", "/api/user/ip-blacklist", {"ip": "10.9.9.9"})
    case("user.ipblacklist.add.err", "PUT", "/api/user/ip-blacklist", {"ip": "not-an-ip"})
    case("user.ipblacklist.del", "DELETE", "/api/user/ip-blacklist", {"ip": "10.9.9.9"})
    case("user.ipblacklist.del.err", "DELETE", "/api/user/ip-blacklist", {"ip": "10.8.8.8"})
    case("user.notification", "GET", "/api/user/notification")
    case("user.notification.err", "GET", "/api/user/notification", token=None)
    case("user.notification.save", "PUT", "/api/user/notification",
         {"webhook": "http://127.0.0.1:5899/hook"})
    case("user.notification.save.err", "PUT", "/api/user/notification",
         {"type": "webhook", "webhook": "http://127.0.0.1:5899/hook"})
    case("user.notification.init", "PUT", "/api/user/notification/init",
         {"webhook": "http://127.0.0.1:5899/hook"}, token=None)
    case("user.notification.init.err", "POST", "/api/user/notification/init", body={}, token=None)
    case("user.avatar", "PUT", "/api/user/avatar", token="user",
         multipart_spec=lambda side: multipart({}, [("avatar", "a.png", PNG)]))
    case("user.avatar.err", "PUT", "/api/user/avatar", token="user",
         multipart_spec=lambda side: multipart({}, []))
    case("user.init.again", "PUT", "/api/user/init",
         {"username": "admin", "password": "admin123"}, token=None)
    case("user.init.err", "PUT", "/api/user/init", {"username": "admin"}, token=None)
    case("user.logout", "POST", "/api/user/logout", token="user2")
    case("user.logout.err", "POST", "/api/user/logout", token=None)


def client_ip_cases(_):
    case("clientip.config", "GET", "/api/system/client-ip/config")
    case("clientip.config.err", "GET", "/api/system/client-ip/config", token=None)
    case("clientip.update", "PUT", "/api/system/client-ip/config", {"trustProxy": "1"})
    case("clientip.update.err", "PUT", "/api/system/client-ip/config", {"trustProxy": "21"})
    case("clientip.diagnose", "GET", "/api/system/client-ip/diagnose")
    case("clientip.diagnose.err", "GET", "/api/system/client-ip/diagnose", token=None)


def retention_cases(_):
    case("retention.config", "PUT", "/api/system/storage-retention/config",
         {"runningInstanceRetentionDays": 3, "cronStatRetentionDays": 9})
    case("retention.config.err", "PUT", "/api/system/storage-retention/config",
         {"cronStatRetentionDays": 9})
    case("retention.preview", "POST", "/api/system/storage-retention/preview",
         {"runningInstanceRetentionDays": 3, "cronStatRetentionDays": 9,
          "dependenceCacheTypes": ["node"], "compactDatabase": False})
    case("retention.preview.err", "POST", "/api/system/storage-retention/preview",
         {"dependenceCacheTypes": "node"})
    case("retention.cleanup", "POST", "/api/system/storage-retention/cleanup",
         {"confirmation": "cleanup", "runningInstanceRetentionDays": 3,
          "cronStatRetentionDays": 9, "dependenceCacheTypes": [], "compactDatabase": False})
    case("retention.cleanup.err", "POST", "/api/system/storage-retention/cleanup",
         {"confirmation": "no", "runningInstanceRetentionDays": 3, "cronStatRetentionDays": 9})


def update_cases(_):
    # The three upgrade/reload routes terminate the panel (Node runs `ql reload`
    # and exits, Go returns and shuts down). They run in the finale, after this
    # module, because they cannot be followed by another request.
    # M8 decision: the upgrade endpoints stay implemented as "only the
    # container image can upgrade" stubs, so the answer differs from a Node
    # panel that actually tries to spawn an upgrade task.
    case("update.system", "PUT", "/api/update/system",
         expect_diff="升级接口保留为容器版空实现（M8 决策）")
    case("update.system.err", "PUT", "/api/update/system", token=None)
    case("update.check", "PUT", "/api/system/update-check")
    case("update.check.err", "PUT", "/api/system/update-check", token=None)


# Routes that stop the panel: compared last, each after a restart.
FAREWELL = [
    ("system.update", "PUT", "/api/system/update",
     "Node 启动下载升级包任务，Go 保留容器版空实现（M8 决策）"),
    ("update.reload", "PUT", "/api/update/reload", None),
    ("update.data", "PUT", "/api/update/data", None),
]


def run_farewell(panels, tokens, normalizers):
    """Compare the three routes that terminate the process, restarting first."""
    for name, method, path, reason in FAREWELL:
        for panel in panels.values():
            panel.stop()
            panel.start()
        for side, panel in panels.items():
            tokens[side]["user"] = ensure_login(panel)

        results = {}
        for side, panel in panels.items():
            try:
                status, _, body = http(panel.base, method, path, body={},
                                       token=tokens[side]["user"], timeout=20)
            except Exception as error:
                status, body = 598, ("transport: %s" % error).encode()
            try:
                parsed = json.loads(body) if body else None
            except Exception:
                parsed = None
            results[side] = (status, parsed)

        comparisons.append(name)
        node_status, node_json = results["node"]
        go_status, go_json = results["go"]
        if node_status != go_status:
            if reason:
                deviations.append(("update", name, method, path, reason, []))
                print("  skip %-46s %s %s  (%s)" % (name, method, path, reason))
                continue
            failures.append(("update", name, method, path,
                             ["status %s != %s" % (node_status, go_status)]))
            print("  FAIL %-46s %s %s" % (name, method, path))
            continue
        if node_json is not None and go_json is not None:
            left = normalizers["node"].walk(node_json)
            right = normalizers["go"].walk(go_json)
            if left != right:
                if reason:
                    deviations.append(("update", name, method, path, reason, []))
                    print("  skip %-46s %s %s  (%s)" % (name, method, path, reason))
                    continue
                failures.append(("update", name, method, path,
                                 ["body %s != %s" % (json.dumps(left, ensure_ascii=False),
                                                     json.dumps(right, ensure_ascii=False))]))
                print("  FAIL %-46s %s %s" % (name, method, path))
                continue
        # Both answered; record whether the process honours it.
        exited = {}
        for side, panel in panels.items():
            deadline = time.time() + 10
            while time.time() < deadline and panel.alive():
                time.sleep(0.2)
            exited[side] = not panel.alive()
        print("  ok   %-46s %s %s  exit node=%s go=%s"
              % (name, method, path, exited["node"], exited["go"]))


PNG = base64.b64decode(
    b"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")


# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------
def normalize_route(method, path):
    """Method plus path with every parameter replaced by *."""
    path = path.split("?")[0]
    parts = []
    for part in path.strip("/").split("/"):
        parameter = part.startswith("{") or part.startswith(":") or part.isdigit() or "." in part
        parts.append("*" if parameter else part)
    return method.upper() + " /" + "/".join(parts)


def inventory_routes():
    import re as _re
    source = os.path.join(GO_ROOT, "docs", "api-inventory.md")
    routes = set()
    try:
        lines = open(source, encoding="utf-8").read().splitlines()
    except OSError:
        return routes
    for line in lines:
        match = _re.match(r"\|\s*\d+\s*\|\s*(GET|POST|PUT|DELETE|PATCH)\s*\|\s*`([^`]+)`", line)
        if match:
            routes.add(normalize_route(match.group(1), match.group(2)))
    return routes


def report_coverage():
    exercised = {normalize_route(item.method, item.path) for item in CASES}
    exercised |= {normalize_route(item[1], item[2]) for item in FAREWELL}
    expected = inventory_routes()
    print("inventory routes : %d" % len(expected))
    print("exercised routes : %d" % len(exercised))
    missing = sorted(expected - exercised)
    extra = sorted(exercised - expected)
    if missing:
        print("\nnot exercised (%d):" % len(missing))
        for route in missing:
            print("  -", route)
    if extra:
        print("\nextra (not in the inventory, %d):" % len(extra))
        for route in extra:
            print("  +", route)
    print("\ncomparison cases: %d over %d routes (+%d farewell routes)"
          % (len(CASES) + len(FAREWELL), len(exercised), len(FAREWELL)))
    return 0 if not missing else 1


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--only", help="run one module only")
    parser.add_argument("--coverage", action="store_true",
                        help="list route coverage against docs/api-inventory.md")
    parser.add_argument("--no-restart", action="store_true")
    args = parser.parse_args()

    if not os.path.isdir(SEED):
        raise SystemExit("seed data directory not found: " + SEED)
    if not os.path.isfile(GO_EXE):
        raise SystemExit("go executable not found: " + GO_EXE)

    build_cases()
    MODULE[0] = "health"

    if args.coverage:
        report_coverage()
        return 0

    prepare_dirs()
    panels = {"node": node_panel(), "go": go_panel()}
    for panel in panels.values():
        panel.start()

    tokens = {}
    try:
        for side, panel in panels.items():
            primary = ensure_login(panel)
            second = ensure_login(panel)
            tokens[side] = {"user": primary, "user2": second, "app_access": None,
                            "open": None}
        # App token for the /open alias: mint it on each side from its own app.
        for side, panel in panels.items():
            status, _, body = http(panel.base, "GET", "/api/apps", token=tokens[side]["user"])
            apps = json.loads(body).get("data") or []
            if apps:
                app = apps[0]
                status, _, body = http(
                    panel.base, "GET",
                    "/api/auth/token?client_id=%s&client_secret=%s"
                    % (app.get("client_id", ""), app.get("client_secret", "")))
                payload = json.loads(body)
                if payload.get("data", {}).get("token"):
                    app_token = payload["data"]["token"]
                    tokens[side]["app_access"] = app_token
                    tokens[side]["open"] = app_token

        normalizers = {
            "node": Normalizer([panels["node"].data_dir, NODE_ROOT]),
            "go": Normalizer([panels["go"].data_dir, GO_ROOT]),
        }

        # Seed the scripts the cron cases run, before any case compares output:
        # a run against a missing file fails differently in each implementation.
        for side, panel in panels.items():
            for filename, content in (
                ("m9-cron.js", "console.log('m9 cron')\n"),
                ("m9-multi.js", "console.log('m9 multi')\n"),
            ):
                http(panel.base, "POST", "/api/scripts",
                     {"filename": filename, "path": ".", "content": content},
                     token=tokens[side]["user"])
        # The import route needs a small archive; reuse the seed export both
        # sides can accept without depending on each other's binary format.
        seed_tgz = os.path.join(os.path.dirname(SEED), "seed.tgz")
        blob = open(seed_tgz, "rb").read() if os.path.isfile(seed_tgz) else b""
        for side in ("node", "go"):
            STATE["system"][side]["import_blob"] = blob

        ordered = []
        for name, entries in MODULE_ORDER:
            for entry in entries:
                ordered.append((name, entry))

        for name, entry in ordered:
            if args.only and name != args.only:
                continue
            MODULE[0] = name
            run_case(entry, panels, tokens, normalizers)

        if not args.only or args.only == "update":
            MODULE[0] = "update"
            run_farewell(panels, tokens, normalizers)

        print("\n%d comparisons, %d failure(s), %d documented deviation(s)"
              % (len(comparisons), len(failures), len(deviations)))
        for module_name, case_name, method, path, reason, _ in deviations:
            print(" ~ [%s] %s %s  %s" % (module_name, method, path, reason))
        for module_name, case_name, method, path, detail in failures:
            print(" - [%s] %s %s %s" % (module_name, method, path, case_name))
            for line in detail:
                print("     " + line)
        return 1 if failures else 0
    finally:
        for panel in panels.values():
            panel.stop()


if __name__ == "__main__":
    sys.exit(main())
