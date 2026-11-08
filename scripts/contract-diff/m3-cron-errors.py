"""M3 task CRUD error cases.

Runs the 18 negative cases against one panel (Node or Go).
Usage: python m3-cron-errors.py http://127.0.0.1:5901 out.json
Assumes the bootstrap admin is admin/admin123 and task id 1 exists.
"""

import json
import sys
import urllib.error
import urllib.request

BASE = sys.argv[1]
OUT = sys.argv[2]


def request(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode()
        try:
            return exc.code, json.loads(raw)
        except json.JSONDecodeError:
            return exc.code, raw


status, login = request("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
token = login["data"]["token"]
cases = {
    "missing-command": ("POST", "/api/crons", {"schedule": "0 0 * * *"}),
    "bad-schedule": ("POST", "/api/crons", {"command": "task a.js", "schedule": "bad rule"}),
    "extra-string-array": (
        "POST",
        "/api/crons",
        {"command": "task a.js", "schedule": "0 0 * * *", "extra_schedules": ["0 1 * * *"]},
    ),
    "unsafe-log-name": (
        "POST",
        "/api/crons",
        {"command": "task a.js", "schedule": "0 0 * * *", "log_name": "../x"},
    ),
    "update-missing-id": (
        "PUT",
        "/api/crons",
        {"id": 99999, "command": "task a.js", "schedule": "0 0 * * *"},
    ),
    "get-missing-id": ("GET", "/api/crons/99999", None),
    "get-bad-id": ("GET", "/api/crons/abc", None),
    "delete-not-array": ("DELETE", "/api/crons", {"id": 1}),
    "duplicate": (
        "POST",
        "/api/crons",
        {"name": "任务A", "command": "task x.js", "schedule": "0 0 * * *"},
    ),
    "view-type-1": (
        "PUT",
        "/api/crons/views",
        {"id": 1, "name": "全部任务", "type": 1},
    ),
    "view-missing-name": ("POST", "/api/crons/views", {"sorts": []}),
    "log-negative-offset": ("GET", "/api/crons/1/log?offset=-1", None),
    "log-bad-limit": ("GET", "/api/crons/1/log?limit=0", None),
    "log-bad-tail": ("GET", "/api/crons/1/log?tail=maybe", None),
    "logs-detail-missing-file": ("GET", "/api/logs/detail?path=x", None),
    "logs-410": ("GET", "/api/logs/whatever.log", None),
    "run-not-array": ("PUT", "/api/crons/run", {"id": 1}),
    "status-missing-status": ("PUT", "/api/crons/status", {"ids": [1]}),
}
results = {}
for name, (method, path, body) in cases.items():
    status, payload = request(method, path, body, token)
    results[name] = {"status": status, "body": payload}
    print(name, status, json.dumps(payload, ensure_ascii=False)[:160])

with open(OUT, "w", encoding="utf-8") as handle:
    json.dump(results, handle, ensure_ascii=False, indent=2)
