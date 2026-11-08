import json, sys

def load(path):
    with open(path, encoding='utf-8') as fh:
        return json.load(fh)

if len(sys.argv) != 3:
    print('usage: compare.py <node.json> <go.json>')
    sys.exit(2)

node = load(sys.argv[1])
go = load(sys.argv[2])

if len(node) != len(go):
    print('length mismatch: node=%d go=%d' % (len(node), len(go)))

diffs = 0
for a, b in zip(node, go):
    if a == b:
        continue
    # ignore the 100 rate-limit steps' bodies beyond status
    if a['step'].startswith('rate-') and b['step'].startswith('rate-'):
        if a['status'] == b['status']:
            continue
    diffs += 1
    print('--- %s %s %s' % (a['step'], a['method'], a['path']))
    print('  node: %s %s' % (a['status'], json.dumps(a['body'], ensure_ascii=False, sort_keys=True)))
    print('  go  : %s %s' % (b['status'], json.dumps(b['body'], ensure_ascii=False, sort_keys=True)))

print('steps: %d, differences: %d' % (len(node), diffs))
sys.exit(1 if diffs else 0)
