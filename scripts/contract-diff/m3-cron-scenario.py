"""M3 task CRUD scenario.

Runs the 24-step task/view scenario against one panel (Node or Go).
Usage: python m3-cron-scenario.py http://127.0.0.1:5901 out.json
Start both panels from the same data/ copy, then normalize timestamp/createdAt/
updatedAt/time/createTime before comparing the JSON files.
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


results = {}


def record(name, method, path, body=None):
    status, payload = request(method, path, body, token)
    results[name] = {"status": status, "body": payload}
    print(name, status)
    return payload


status, login = request("POST", "/api/user/login", {"username": "admin", "password": "admin123"})
token = login["data"]["token"]
results["login"] = {"status": status, "body": {"code": login["code"]}}

record("list-before", "GET", "/api/crons")

complex_task = {
    "name": "复杂任务",
    "command": "task sub/complex.js --flag 'x'",
    "schedule": "0 3 * * *",
    "labels": ["l1", "l2"],
    "sub_id": 42,
    "extra_schedules": [
        {"schedule": "30 4 * * *"},
        {"schedule": "0 5 * * 1-5", "foo": "bar"},
    ],
    "task_before": "echo 'a'\necho b;",
    "task_after": "echo done",
    "log_name": "complex_log",
    "allow_multiple_instances": 1,
    "work_dir": "/tmp/a b",
}
created = record("create-complex", "POST", "/api/crons", complex_task)
complex_id = created["data"]["id"]

simple = record(
    "create-simple",
    "POST",
    "/api/crons",
    {"name": "临时任务", "command": "task q.js", "schedule": "@once"},
)
simple_id = simple["data"]["id"]

record("detail-complex", "GET", f"/api/crons/{complex_id}")

record(
    "update-complex",
    "PUT",
    "/api/crons",
    {
        "id": complex_id,
        "name": "复杂任务改",
        "command": "task sub/complex2.py",
        "schedule": "*/10 * * * *",
        "labels": ["l3"],
        "sub_id": None,
        "extra_schedules": None,
        "task_before": 'echo "q"',
        "task_after": "",
        "log_name": "",
        "allow_multiple_instances": 0,
        "work_dir": "",
    },
)

record("pin", "PUT", "/api/crons/pin", [complex_id])
record("add-labels", "POST", "/api/crons/labels", {"ids": [complex_id], "labels": ["l3", "l1"]})
record("remove-labels", "DELETE", "/api/crons/labels", {"ids": [complex_id], "labels": ["l2"]})
record("disable-simple", "PUT", "/api/crons/disable", [simple_id])
record("enable-simple", "PUT", "/api/crons/enable", [simple_id])
record("unpin", "PUT", "/api/crons/unpin", [complex_id])
record("delete-simple", "DELETE", "/api/crons", [simple_id])

record("list-after", "GET", "/api/crons")
record("list-page", "GET", "/api/crons?page=1&size=2")
record("list-status", "GET", '/api/crons?filters={"status":[1]}')

view_a = record(
    "view-create-a",
    "POST",
    "/api/crons/views",
    {
        "name": "视图A",
        "sorts": [{"property": "name", "type": "ASC"}],
        "filters": [{"property": "status", "operation": "In", "value": [1]}],
        "filterRelation": "and",
    },
)
view_b = record(
    "view-create-b",
    "POST",
    "/api/crons/views",
    {
        "name": "视图B",
        "sorts": None,
        "filters": [{"property": "name", "operation": "Reg", "value": "任务"}],
        "filterRelation": "or",
    },
)
record("view-list", "GET", "/api/crons/views")
record(
    "view-update",
    "PUT",
    "/api/crons/views",
    {
        "id": view_b["data"]["id"],
        "name": "视图B2",
        "sorts": [{"property": "createdAt", "type": "DESC"}],
        "filters": [{"property": "name", "operation": "NotReg", "value": "x"}],
        "filterRelation": "and",
    },
)
record("view-move", "PUT", "/api/crons/views/move", {"id": view_b["data"]["id"], "fromIndex": 1, "toIndex": 0})
record("view-disable", "PUT", "/api/crons/views/disable", [view_a["data"]["id"]])
record("view-enable", "PUT", "/api/crons/views/enable", [view_a["data"]["id"]])
record("view-delete", "DELETE", "/api/crons/views", [view_b["data"]["id"]])
record("view-list-final", "GET", "/api/crons/views")

with open(OUT, "w", encoding="utf-8") as handle:
    json.dump(results, handle, ensure_ascii=False, indent=2)
