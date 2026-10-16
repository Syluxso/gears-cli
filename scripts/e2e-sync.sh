#!/usr/bin/env bash
# End-to-end: gears-api (dev tenant, local Postgres 5441), a workspace root
# with project repos under projects/ (the only .gears is the root's, GRS-40),
# and a standalone clone of one project elsewhere, as another machine has it.
# Proves login, project manifest, reconcile, push, pull across caches.
# Usage: scripts/e2e-sync.sh   (from gears-cli; needs ../gears-api and docker byz-kan-db up)
set -euo pipefail
CLI_DIR="$(cd "$(dirname "$0")/.." && pwd)"
API_DIR="$CLI_DIR/../gears-api"
WORK="$(mktemp -d)"
export HOME="$WORK/home"; mkdir -p "$HOME"
# Go reads USERPROFILE, not HOME, on Windows; without this the run would
# write the real ~/.gears/credentials.json.
export USERPROFILE="$HOME"
export GEARS_API="http://127.0.0.1:8221"
PORT=8221

cleanup() {
  [ -n "${WATCH_PID:-}" ] && kill "$WATCH_PID" 2>/dev/null || true
  [ -n "${FILES_PID:-}" ] && kill "$FILES_PID" 2>/dev/null || true
  [ -n "${ORCH_PID:-}" ] && kill "$ORCH_PID" 2>/dev/null || true
  [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null || true
}
trap cleanup EXIT
fail() { echo "FAIL: $*"; exit 1; }

( cd "$CLI_DIR" && go build -o "$WORK/gears.exe" . )
( cd "$API_DIR" && go build -o "$WORK/gears-api.exe" . )
G="$WORK/gears.exe"

ORG=$("$G" --version >/dev/null; python -c "import uuid;print(uuid.uuid4())")
TEN=$(python -c "import uuid;print(uuid.uuid4())")
# A stand-in byz-files (GRS-20): uploads fail while $FILES_FAIL exists.
FILES_PORT=8222; FILES_FAIL="$WORK/files-fail"; FILES_COUNT="$WORK/files-count"
python -I "$CLI_DIR/scripts/fake-byz-files.py" $FILES_PORT e2e-files-token "$(cygpath -w "$FILES_FAIL" 2>/dev/null || echo "$FILES_FAIL")" "$(cygpath -w "$FILES_COUNT" 2>/dev/null || echo "$FILES_COUNT")" >> "$WORK/files.log" 2>&1 &
FILES_PID=$!
for i in $(seq 1 20); do curl -sf "http://127.0.0.1:$FILES_PORT/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "http://127.0.0.1:$FILES_PORT/healthz" >/dev/null || { echo "fake byz-files did not start"; cat "$WORK/files.log"; exit 1; }
uploads() { cat "$FILES_COUNT" 2>/dev/null || echo 0; }
# A stand-in byz-orchestrator (GRS-56): gears-api calls it with a fixed token.
ORCH_PORT=8223
python -I "$CLI_DIR/scripts/fake-orchestrator.py" $ORCH_PORT e2e-orch-token >> "$WORK/orch.log" 2>&1 &
ORCH_PID=$!
for i in $(seq 1 20); do curl -sf "http://127.0.0.1:$ORCH_PORT/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "http://127.0.0.1:$ORCH_PORT/healthz" >/dev/null || { echo "fake orchestrator did not start"; cat "$WORK/orch.log"; exit 1; }

# start_api: this script's own throwaway gears-api (GRS-51 stops and
# restarts it to prove an offline write queues).
start_api() {
  GEARS_API_PORT=$PORT PORT=$PORT \
  DB_URL='postgres://db:db@127.0.0.1:5441/gears?sslmode=disable' \
  GEARS_PAT_SECRET=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
  GEARS_DEV_TENANT="$ORG:$TEN" BYZ_KAFKA_ENABLED=false \
  BYZ_FILES_URL="http://127.0.0.1:$FILES_PORT" BYZ_FILES_TOKEN=e2e-files-token \
  BYZ_ORCHESTRATOR_URL="http://127.0.0.1:$ORCH_PORT" BYZ_ORCHESTRATOR_TOKEN=e2e-orch-token \
  "$WORK/gears-api.exe" >> "$WORK/api.log" 2>&1 &
  API_PID=$!
  for i in $(seq 1 30); do curl -sf "$GEARS_API/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
  curl -sf "$GEARS_API/healthz" >/dev/null || { echo "api did not start"; cat "$WORK/api.log"; exit 1; }
}
start_api

WS="$WORK/ws"; A="$WS/projects/alpha"; C="$WORK/clone/alpha"
# Workspace root (not itself a repo, like ddev/gears) with one project repo.
mkdir -p "$A" "$C"
( cd "$A" && git init -q -b main . && git remote add origin git@github.com:example/alpha.git )
# A second clone of alpha elsewhere, as another machine would have it.
( cd "$C" && git init -q -b main . && git remote add origin https://github.com/example/alpha.git )
# pid <slug>: a project's id from the root manifest.
pid() { ( cd "$WS" && "$G" projects list --json ) | python -c "import json,sys; print([p['id'] for p in json.load(sys.stdin)['projects'] if p['slug']=='$1'][0])"; }

echo "== init at the workspace root scans projects/ (not logged in yet: offline ids)"
( cd "$WS" && "$G" init --prefix WS --name claude-code+local )
[ "$(tr -d '\r\n' < "$WS/.gears/.gearbox/agent")" = "claude-code+local" ] || fail "init did not save the agent name"
[ -f "$WS/.gears/projects.yaml" ] || fail "no manifest"
ALPHA_ID=$(pid alpha)
WS_ID=$(grep '^id:' "$WS/.gears/project.yaml" | awk '{print $2}')

echo "== init inside projects/alpha is refused; no nested .gears"
if ( cd "$A" && "$G" init >"$WORK/nested.out" 2>&1 ); then fail "nested init accepted"; fi
grep -q "gears projects scan at" "$WORK/nested.out" || { cat "$WORK/nested.out"; fail "refusal does not point at the root"; }
[ ! -e "$A/.gears" ] || fail "nested .gears created"

echo "== login from the workspace root registers the workspace and its projects"
( cd "$WS" && "$G" login --email a@b.c --password x --label claude-code+local )
TOKEN=$(python -c "import json,sys; print(json.load(open(sys.argv[1]))['hosts']['$GEARS_API']['token'])" "$HOME/.gears/credentials.json")
projects() { curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/projects?workspaceId=$WS_ID"; }
PJ="import json,sys; d=json.load(sys.stdin); d=d.get('projects',d) if isinstance(d,dict) else d"
projects | python -c "$PJ; a=[p for p in d if p['id']=='$ALPHA_ID']; assert len(a)==1 and a[0]['gitRemote']=='github.com/example/alpha', d; assert '$WS_ID' not in [p['id'] for p in d], 'root became a project'; print('ok: alpha in the account under the manifest id')"

echo "== write inside projects/alpha (resolves to the root binding), sync from there"
( cd "$A" && "$G" desk send --type handoff --to claude-code+local "Tried A. Result B. Next C." >/dev/null )
( cd "$A" && "$G" session new --kind done "alpha did a thing" --json ) | python -c "import json,sys; e=json.load(sys.stdin); assert e['projectId']=='$ALPHA_ID', e; print('ok: entry tagged alpha')"
( cd "$A/" && mkdir -p src && cd src && "$G" sync )
[ ! -e "$A/.gears" ] || fail "a .gears appeared under projects/"

echo "== fresh standalone clone binds by remote and pulls"
( cd "$C" && "$G" init --name grok-build+ubuntu >/dev/null && "$G" login --email a@b.c --password x --label grok-build+ubuntu && "$G" sync )
CLONE_ID=$(grep '^id:' "$C/.gears/project.yaml" | awk '{print $2}')
[ "$CLONE_ID" = "$ALPHA_ID" ] || fail "clone did not adopt alpha's id ($CLONE_ID vs $ALPHA_ID)"
( cd "$C" && "$G" -a claude-code+local desk list --json | grep -q "Tried A" ) || fail "clone did not receive the handoff"
( cd "$C" && "$G" session list --json ) | python -c "import json,sys; e=[x for x in json.load(sys.stdin)['entries'] if x['text']=='alpha did a thing']; assert len(e)==1 and e[0].get('projectId')=='$ALPHA_ID', e; print('ok: clone has the entry, tagged alpha')"

echo "== clone answers; workspace root sees it after sync, tagged alpha"
( cd "$C" && "$G" session new --kind decision "clone decided" >/dev/null && "$G" sync >/dev/null )
( cd "$WS" && "$G" sync >/dev/null && "$G" session list --json ) | python -c "import json,sys; e=[x for x in json.load(sys.stdin)['entries'] if x['text']=='clone decided']; assert len(e)==1 and e[0].get('projectId')=='$ALPHA_ID', e; print('ok: root has clone entry')"

echo "== GRS-44: artifact delete in the single-root model: three alpha artifacts, deleted three ways, gone everywhere"
# All three are made inside projects/alpha: one cache (the root's), tagged
# alpha. One is deleted by short id from projects/alpha, one by file name
# from a subdirectory of it, one by full id (--json, the MCP verb) at the root.
DB="$WS/.gears/.gearbox/local.db"
tombs() { python -c "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(' '.join(sorted(r[0] for r in c.execute(\"SELECT id FROM sync_deletes WHERE tbl='artifacts'\"))))" "$DB"; }
for n in 1 2 3; do
  ( cd "$A" && "$G" artifact new free "Doomed note $n" --json ) | python -c "import json,sys; a=json.load(sys.stdin); assert a['projectId']=='$ALPHA_ID' and '.gears' in a['path'] and 'projects' not in a['path'].split('.gears')[0][len('$WS'):], a"
  [ -f "$WS/.gears/artifacts/free--doomed-note-$n.md" ] || fail "artifact $n not in the root cache"
done
echo "ok: three artifacts tagged alpha, in the root cache"
( cd "$A" && "$G" sync >/dev/null )
( cd "$C" && "$G" sync >/dev/null && [ "$("$G" artifact list | grep -c "doomed-note")" = 3 ] ) || fail "clone did not receive the three artifacts"
for n in 1 2 3; do
  [ -f "$C/.gears/artifacts/free--doomed-note-$n.md" ] || fail "clone has no file for doomed-note-$n"
done
artid() { ( cd "$WS" && "$G" artifact list --json ) | python -c "import json,sys; print([a['id'] for a in json.load(sys.stdin)['artifacts'] if a['file']=='$1'][0])"; }
ID1=$(artid free--doomed-note-1.md); ID2=$(artid free--doomed-note-2.md); ID3=$(artid free--doomed-note-3.md)
( cd "$A" && "$G" artifact delete "${ID1: -8}" | grep -q "deleted ${ID1: -8}" ) || fail "delete by short id"
( cd "$A/src" && "$G" artifact delete free--doomed-note-2.md | grep -q "free--doomed-note-2.md" ) || fail "delete by file name"
( cd "$WS" && "$G" artifact delete "$ID3" --json ) | python -c "import json,sys; a=json.load(sys.stdin); assert a['id']=='$ID3' and a['projectId']=='$ALPHA_ID' and not a['filePresent'], a"
for n in 1 2 3; do
  [ ! -f "$WS/.gears/artifacts/free--doomed-note-$n.md" ] || fail "root still has doomed-note-$n"
done
( cd "$WS" && "$G" artifact reconcile | grep -q "rows and files agree" ) || fail "root reconcile regenerated or adopted"
( cd "$A" && "$G" artifact list --json | grep -q "doomed-note" ) && fail "root still lists a deleted artifact"
[ "$(tombs)" = "$(printf '%s\n' "$ID1" "$ID2" "$ID3" | sort | tr '\n' ' ' | sed 's/ $//')" ] || fail "tombstones: $(tombs)"
echo "ok: rows and files gone from the root cache; three tombstones wait for sync"
( cd "$A" && "$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0, r; print('ok: deletes pushed from projects/alpha')" )
[ -z "$(tombs)" ] || fail "tombstones not cleared after push: $(tombs)"
for id in "$ID1" "$ID2" "$ID3"; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/artifacts/$id")
  [ "$CODE" = "404" ] || fail "API still serves $id ($CODE)"
done
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/workspaces/$WS_ID/sync?cursor=0" | python -c "
import json,sys
ch=json.load(sys.stdin)['changes']
for i in ['$ID1','$ID2','$ID3']:
    d=[c for c in ch if c['table']=='artifacts' and c['id']==i and c.get('deleted')]
    assert d, ('no soft-delete change for', i)
print('ok: API answers 404 and its change log carries a soft delete for each')"
( cd "$C" && "$G" sync >/dev/null )
for n in 1 2 3; do
  [ ! -f "$C/.gears/artifacts/free--doomed-note-$n.md" ] || fail "clone kept doomed-note-$n after the pulled delete"
done
( cd "$C" && "$G" artifact reconcile | grep -q "rows and files agree" ) || fail "clone reconcile regenerated or adopted"
( cd "$C" && "$G" artifact list --json | grep -q "doomed-note" ) && fail "clone still lists a deleted artifact"
# Later syncs on either side bring nothing back.
for d in "$C" "$WS" "$C" "$A"; do ( cd "$d" && "$G" sync >/dev/null ); done
( cd "$WS" && "$G" artifact list --json | grep -q "doomed-note" ) && fail "root re-adopted a deleted artifact"
( cd "$C" && "$G" artifact list --json | grep -q "doomed-note" ) && fail "clone re-adopted a deleted artifact"
ls "$WS/.gears/artifacts" "$C/.gears/artifacts" | grep -q "doomed-note" && fail "a deleted file came back"
echo "ok: rows and files gone on both machines and stay gone; API rows soft-deleted"

echo "== GRS-40: one file name per workspace; a colliding row from elsewhere is cached beside, not over"
SAME=free--same-name.md
# The clone makes alpha's free--same-name.md before it has seen the root's.
( cd "$C" && "$G" artifact new free "Same name" >/dev/null && printf '\nalpha body\n' >> ".gears/artifacts/$SAME" && "$G" artifact reconcile >/dev/null )
( cd "$WS" && "$G" artifact new free "Same name" >/dev/null && printf '\nroot body\n' >> ".gears/artifacts/$SAME" && "$G" artifact reconcile >/dev/null && "$G" sync >/dev/null )
if ( cd "$A" && "$G" artifact new free "Same name" >/dev/null 2>&1 ); then fail "duplicate name accepted inside the workspace"; fi
for d in "$C" "$WS" "$C" "$WS"; do
  ( cd "$d" && "$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); bad=[n for n in r['notes'] if 'UNIQUE' in n or 'apply' in n]; assert not bad and r['failed']==0, r; print('ok: sync', '$d'.rsplit('/',2)[-2:], 'pulled', r['pulled'], 'applied', r['applied'], [n for n in r['notes'] if 'cached as' in n])" )
done
grep -q "root body" "$WS/.gears/artifacts/$SAME" && ! grep -q "alpha body" "$WS/.gears/artifacts/$SAME" || fail "root file overwritten"
[ -f "$WS/.gears/artifacts/free--same-name--alpha.md" ] || fail "root has no copy of alpha's artifact"
grep -q "alpha body" "$C/.gears/artifacts/$SAME" && ! grep -q "root body" "$C/.gears/artifacts/$SAME" || fail "clone file overwritten"
ls "$C/.gears/artifacts/" | grep -q "^free--same-name--.*\.md$" || fail "clone has no copy of the root's artifact"
ROOT_SAME=$(cd "$WS" && "$G" artifact list --json | python -c "import json,sys; print([a['id'] for a in json.load(sys.stdin)['artifacts'] if a['file']=='$SAME'][0])")
ALPHA_SAME=$(cd "$C" && "$G" artifact list --json | python -c "import json,sys; print([a['id'] for a in json.load(sys.stdin)['artifacts'] if a['file']=='$SAME'][0])")
[ "$ROOT_SAME" != "$ALPHA_SAME" ] || fail "root and alpha share an id"
( cd "$WS" && "$G" artifact list --json ) | python -c "import json,sys; a=[x for x in json.load(sys.stdin)['artifacts'] if x['id']=='$ALPHA_SAME']; assert len(a)==1 and a[0]['file']=='free--same-name--alpha.md' and a[0]['slug']=='same-name' and a[0].get('projectId')=='$ALPHA_ID', a; print('ok: root caches alpha copy as', a[0]['file'])"
# Without a file store the body cannot travel and the root holds a seeded
# stub; the stub must never be pushed over the clone's row.
ALPHA_HASH=$(python -c "import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],'rb').read().replace(b'\r\n',b'\n')).hexdigest()[:16])" "$C/.gears/artifacts/$SAME")
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/artifacts/$ALPHA_SAME" | python -c "import json,sys; a=json.load(sys.stdin); a=a.get('artifact',a); assert a.get('projectId')=='$ALPHA_ID' and a.get('slug')=='same-name' and a.get('bodyHash')=='$ALPHA_HASH', a; print('ok: alpha artifact carries projectId, slug not renamed, body hash is the clone\'s')"
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/artifacts/$ROOT_SAME" | python -c "import json,sys; a=json.load(sys.stdin); a=a.get('artifact',a); assert not a.get('projectId'), a; print('ok: root artifact has no projectId')"

echo "== GRS-40: projects scan adds a new repo; sync creates it once, idempotent, remote refreshed"
B="$WS/projects/beta"; mkdir -p "$B"
( cd "$B" && git init -q -b main . && git remote add origin git@github.com:example/beta.git )
( cd "$A" && "$G" projects scan )
BETA_ID=$(pid beta)
projects | python -c "$PJ; assert '$BETA_ID' not in [p['id'] for p in d], d"
( cd "$WS" && "$G" sync >/dev/null )
BETA1=$(projects | python -c "$PJ; b=[p for p in d if p['id']=='$BETA_ID']; assert len(b)==1 and b[0]['gitRemote']=='github.com/example/beta', d; print(b[0]['updatedAt'])")
( cd "$WS" && "$G" sync >/dev/null && "$G" sync >/dev/null )
BETA2=$(projects | python -c "$PJ; b=[p for p in d if p['id']=='$BETA_ID']; assert len(b)==1, d; print(b[0]['updatedAt'])")
[ "$BETA1" = "$BETA2" ] || fail "re-sync rewrote the project ($BETA1 -> $BETA2)"
( cd "$B" && git remote set-url origin https://github.com/example/beta-moved.git && "$G" sync >/dev/null )
projects | python -c "$PJ; b=[p for p in d if p['id']=='$BETA_ID']; assert len(b)==1 and b[0]['gitRemote']=='github.com/example/beta-moved', d; assert '$WS_ID' not in [p['id'] for p in d], 'root became a project'; print('ok: beta created once, idempotent, remote refreshed; root is not a project row')"

echo "== GRS-40: a repo that goes is marked missing, kept, and left alone by sync"
mv "$B" "$WORK/beta-away"
( cd "$WS" && "$G" projects scan | grep -q "missing  projects/beta" ) || fail "beta not reported missing"
( cd "$WS" && "$G" projects list --json ) | python -c "import json,sys; b=[p for p in json.load(sys.stdin)['projects'] if p['slug']=='beta']; assert len(b)==1 and b[0].get('missing') and b[0]['id']=='$BETA_ID', b; print('ok: beta kept, missing')"
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0 and r['projects']==1, r; print('ok: sync checked', r['projects'], 'project(s)')"

echo "== GRS-40: an id minted offline gives way to the API's id for a known remote"
GAMMA_API=$(python -c "import uuid;print(uuid.uuid4())")
curl -sf -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"id\":\"$GAMMA_API\",\"workspaceId\":\"$WS_ID\",\"slug\":\"gamma\",\"gitRemote\":\"git@github.com:example/gamma.git\"}" \
  "$GEARS_API/api/v1/projects" >/dev/null || fail "could not create gamma on the API"
GM="$WS/projects/gamma"; mkdir -p "$GM"
( cd "$GM" && git init -q -b main . && git remote add origin https://github.com/example/gamma.git )
( cd "$WS" && "$G" projects scan --offline >/dev/null )
GAMMA_MINTED=$(pid gamma)
[ "$GAMMA_MINTED" != "$GAMMA_API" ] || fail "offline scan reached the API"
( cd "$GM" && "$G" session new --kind done "gamma work" >/dev/null )
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0 and any('adopted' in n for n in r['notes']), r; print('ok:', [n for n in r['notes'] if 'adopted' in n][0])"
[ "$(pid gamma)" = "$GAMMA_API" ] || fail "manifest did not adopt the API id"
( cd "$WS" && "$G" session list --json ) | python -c "import json,sys; e=[x for x in json.load(sys.stdin)['entries'] if x['text']=='gamma work']; assert len(e)==1 and e[0].get('projectId')=='$GAMMA_API', e; print('ok: gamma entry retagged')"
projects | python -c "$PJ; ids=[p['id'] for p in d]; assert '$GAMMA_API' in ids and '$GAMMA_MINTED' not in ids, d; print('ok: no second gamma on the API')"

echo "== GRS-46: status inside projects/gamma is the workspace report, with the API's list"
( cd "$GM" && "$G" status --json ) | python -c "
import json,sys
s=json.load(sys.stdin); ps={p['slug']:p for p in s['projects']}
assert s['api']['state']=='online', s['api']
assert ps['gamma']['current'] and ps['gamma']['onApi'] and ps['gamma']['id']=='$GAMMA_API', ps['gamma']
assert ps['alpha']['onApi'] and ps['alpha']['git']['branch']=='main' and not ps['alpha'].get('current'), ps['alpha']
assert not ps['beta']['onDisk'] and any('missing' in n for n in ps['beta']['notes']), ps['beta']
print('ok: status', len(ps), 'projects, api', s['api']['state'], s['api']['projects'])"
( cd "$GM" && GEARS_API=http://127.0.0.1:1 "$G" status ) | grep -q "api: not logged in" || fail "status for a host with no token does not say so"

echo "== GRS-47: cards: two machines mint the same number offline; the API's numbers win on both"
# Both caches have synced (states adopted, no cards yet), so both propose 1.
# Card writes push at once since GRS-51; GEARS_CARD_SYNC=off keeps these two
# local, as two machines offline would.
newcard() { "$G" card new "$1" -t task --set "task.what=$1" --set task.soThat=e2e --accept "syncs, proven by e2e-sync" --tag e2e --json | python -c "import json,sys; print(json.load(sys.stdin)['key'])"; }
K_ROOT=$(cd "$WS" && GEARS_CARD_SYNC=off newcard "Root card")
K_CLONE=$(cd "$C" && GEARS_CARD_SYNC=off newcard "Clone card")
[ "$K_ROOT" = "WS-1" ] && [ "$K_CLONE" = "WS-1" ] || fail "offline proposals: root $K_ROOT clone $K_CLONE (want WS-1 both)"
echo "ok: both caches proposed WS-1 offline"
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['cards']=='synced' and r['failed']==0, r; print('ok: root sync cards', r['cards'])"
( cd "$C" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['cards']=='synced' and r['failed']==0 and r['cardsRenumbered']==1 and any('WS-1 is WS-2' in n for n in r['notes']), r; print('ok: clone renumbered:', [n for n in r['notes'] if 'renumber' in n or ' is WS-' in n])"
( cd "$WS" && "$G" sync >/dev/null )
keys() { "$G" card list --all --json | python -c "import json,sys; print(' '.join(sorted(c['key']+'='+c['title'].replace(' ','_') for c in json.load(sys.stdin)['cards'])))"; }
KR=$(cd "$WS" && keys); KC=$(cd "$C" && keys)
[ "$KR" = "WS-1=Root_card WS-2=Clone_card" ] || fail "root keys: $KR"
[ "$KC" = "$KR" ] || fail "clone keys ($KC) differ from root ($KR)"
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/workspaces/$WS_ID/board" | python -c "import json,sys; b=json.load(sys.stdin); k=sorted(c['key'] for c in b['cards']); assert k==['WS-1','WS-2'] and b['prefix']=='WS', b; print('ok: API board', k)"
echo "ok: distinct keys, both caches agree: $KR"
# A new card on the clone proposes past what it has seen.
[ "$(cd "$C" && "$G" card list --all --json | python -c "import json,sys; print(json.load(sys.stdin)['prefix'])")" = "WS" ] || fail "clone prefix is not the workspace's"

echo "== GRS-47: a comment, a checklist item and a move on the root reach the clone; card show matches"
( cd "$WS" && "$G" card comment WS-2 "Root looked at the clone's card" >/dev/null \
  && "$G" card check WS-2 "Clone sees the comment" >/dev/null \
  && "$G" card check WS-2 --op tick 1 >/dev/null \
  && "$G" card move WS-2 "In Progress" >/dev/null \
  && "$G" card link WS-1 WS-2 --kind blocks >/dev/null \
  && "$G" sync >/dev/null )
( cd "$C" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0 and not [n for n in r['notes'] if 'apply' in n], r; print('ok: clone pulled', r['pulled'], 'applied', r['applied'])"
SHOWNORM="import json,sys; c=json.load(sys.stdin); c['events']=sorted(json.dumps(e,sort_keys=True) for e in c['events']); print(json.dumps(c,sort_keys=True))"
for k in WS-1 WS-2; do
  R=$(cd "$WS" && "$G" card show $k --json | python -c "$SHOWNORM")
  CL=$(cd "$C" && "$G" card show $k --json | python -c "$SHOWNORM")
  [ "$R" = "$CL" ] || { echo "root:  $R"; echo "clone: $CL"; fail "card show $k differs between root and clone"; }
done
( cd "$C" && "$G" card show WS-2 --json ) | python -c "
import json,sys
c=json.load(sys.stdin)
assert c['state']=='In Progress', c['state']
assert [x['body'] for x in c['comments']]==['Root looked at the clone\'s card'], c['comments']
it=c['checklists'][0]['items']; assert len(it)==1 and it[0]['done'] and it[0]['title']=='Clone sees the comment', it
assert c['blockedBy']==['WS-1'], c['blockedBy']
assert any(e['kind']=='moved' for e in c['events']) and any(e['kind']=='created' for e in c['events']), c['events']
print('ok: card show WS-1 and WS-2 identical on root and clone (state, comment, checklist, link, activity)')"

echo "== GRS-47: a card deleted on the clone is gone on the root; its number is not reused"
( cd "$C" && "$G" card delete WS-1 >/dev/null && "$G" sync >/dev/null )
( cd "$WS" && "$G" sync >/dev/null )
( cd "$WS" && "$G" card list --all --json ) | python -c "import json,sys; k=[c['key'] for c in json.load(sys.stdin)['cards']]; assert k==['WS-2'], k; print('ok: root lists', k)"
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/workspaces/$WS_ID/board" | python -c "import json,sys; k=[c['key'] for c in json.load(sys.stdin)['cards']]; assert k==['WS-2'], k; print('ok: API board', k)"
K3=$(cd "$WS" && newcard "After delete")
[ "$K3" = "WS-3" ] || fail "number reused after delete: $K3"
( cd "$WS" && "$G" sync >/dev/null ) && ( cd "$C" && "$G" sync >/dev/null )
[ "$(cd "$C" && keys)" = "WS-2=Clone_card WS-3=After_delete" ] || fail "clone after delete: $(cd "$C" && keys)"
echo "ok: deletes propagate both ways; WS-3 is new on both"

echo "== GRS-51: card new pushes at once: the API's key, on the API, no gears sync"
DBC="$C/.gears/.gearbox/local.db"
sstate() { python -c "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); r=c.execute(\"SELECT value FROM sync_state WHERE key=?\",(sys.argv[2],)).fetchone(); print(r[0] if r else '')" "$1" "$2"; }
K4=$(cd "$WS" && newcard "Instant card")
[ "$K4" = "WS-4" ] || fail "write-through key: $K4 (want WS-4)"
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/workspaces/$WS_ID/board" | python -c "import json,sys; k=[c['key'] for c in json.load(sys.stdin)['cards']]; assert 'WS-4' in k, k; print('ok: WS-4 on the API board right after card new', k)"

echo "== GRS-51: the clone watches the board; a move on the root lands in the clone's cache within seconds"
CUR_BEFORE=$(sstate "$DBC" cursor)
( cd "$C" && exec "$G" watch > "$WORK/watch.out" 2> "$WORK/watch.err" ) &
WATCH_PID=$!
for i in $(seq 1 40); do grep -q "board stream connected" "$WORK/watch.err" 2>/dev/null && break; sleep 0.25; done
grep -q "board stream connected" "$WORK/watch.err" || { cat "$WORK/watch.err"; fail "watch did not connect"; }
for i in $(seq 1 40); do [ "$(cd "$C" && GEARS_CARD_SYNC=off "$G" card list --all --json | python -c "import json,sys; print(any(c['key']=='WS-4' for c in json.load(sys.stdin)['cards']))")" = True ] && break; sleep 0.25; done
echo "ok: watch connected; $(cat "$WORK/watch.err" | tr '\n' ' ')"
T0=$(python -c "import time; print(time.time())")
( cd "$WS" && "$G" card move WS-4 Testing >/dev/null )
cache_state() { ( cd "$C" && GEARS_CARD_SYNC=off "$G" card show WS-4 --json ) | python -c "import json,sys; print(json.load(sys.stdin)['state'])"; }
for i in $(seq 1 40); do [ "$(cache_state)" = "Testing" ] && break; sleep 0.25; done
[ "$(cache_state)" = "Testing" ] || { cat "$WORK/watch.err" "$WORK/watch.out"; fail "clone cache did not get the move"; }
python -c "import time; print('ok: clone cache has WS-4 in Testing %.1fs after the move (no gears sync, read with card sync off)' % (time.time()-$T0))"
for i in $(seq 1 20); do grep -q "WS-4 moved → Testing" "$WORK/watch.out" && break; sleep 0.25; done
grep -q "WS-4 moved → Testing" "$WORK/watch.out" || { cat "$WORK/watch.out"; fail "watch did not print the move"; }
echo "ok: watch printed: $(grep "WS-4 moved" "$WORK/watch.out" | head -1)"
[ "$(sstate "$DBC" cursor)" = "$CUR_BEFORE" ] || fail "the card stream moved the main cursor ($CUR_BEFORE -> $(sstate "$DBC" cursor))"
[ -n "$(sstate "$DBC" cards_cursor)" ] || fail "no cards_cursor"
echo "ok: main cursor untouched ($CUR_BEFORE); cards_cursor $(sstate "$DBC" cards_cursor)"
kill "$WATCH_PID" 2>/dev/null || true; wait "$WATCH_PID" 2>/dev/null || true; WATCH_PID=""
sleep 0.5
kill -0 "$WATCH_PID" 2>/dev/null && fail "watcher still running"
# Only this script's binary (under $WORK) counts; never a gears.exe elsewhere.
LEFT=$(powershell -NoProfile -Command "@(Get-Process gears -ErrorAction SilentlyContinue | Where-Object { \$_.Path -like '$(cygpath -w "$WORK")*' }).Count" 2>/dev/null | tr -d '\r')
[ "${LEFT:-0}" = "0" ] || fail "watcher still running ($LEFT)"
echo "ok: watcher stopped"

echo "== GRS-51: without a watcher, a card read pulls first (backstop)"
( cd "$WS" && "$G" card comment WS-4 "seen by the clone on read" >/dev/null )
( cd "$C" && "$G" card show WS-4 --json ) | python -c "import json,sys; c=json.load(sys.stdin); assert any(x['body']=='seen by the clone on read' for x in c['comments']) and not c.get('stale'), c; print('ok: clone card show pulled the comment first')"

echo "== GRS-51: offline write stands locally with one note, then goes up"
kill "$API_PID"; wait "$API_PID" 2>/dev/null || true; API_PID=""
( cd "$WS" && "$G" card comment WS-4 "written while the API was down" > "$WORK/off.out" 2> "$WORK/off.err" ) || fail "offline write failed"
grep -q "written while the API was down" "$WORK/off.out" || fail "offline comment not printed"
[ "$(wc -l < "$WORK/off.err")" -eq 1 ] && grep -q "^gears: saved locally; not pushed" "$WORK/off.err" || { cat "$WORK/off.err"; fail "offline note"; }
echo "ok: offline note: $(cat "$WORK/off.err")"
( cd "$WS" && "$G" card list --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o.get('stale') is True, o.get('stale'); print('ok: offline card list answers from the cache, stale: true')"
start_api
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0, r; print('ok: sync after the outage pushed', r['pushed'])"
( cd "$C" && "$G" card show WS-4 --json ) | python -c "import json,sys; c=json.load(sys.stdin); assert any(x['body']=='written while the API was down' for x in c['comments']), c['comments']; print('ok: the queued comment reached the clone')"

echo "== GRS-20: a failed content upload is retried on the next sync, then reaches the clone"
( cd "$WS" && "$G" artifact new free "Retry body" >/dev/null && printf '\nretry body line\n' >> .gears/artifacts/free--retry-body.md && "$G" artifact reconcile >/dev/null )
touch "$FILES_FAIL"; BEFORE=$(uploads)
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['contentUploaded']==0 and any('free--retry-body.md' in n and '502' in n for n in r['notes']), r; print('ok: upload failed:', [n for n in r['notes'] if 'retry-body' in n][0])"
rm -f "$FILES_FAIL"
sleep 1
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['contentUploaded']>=1 and not any('retry-body' in n for n in r['notes']), r; print('ok: next sync uploaded', r['contentUploaded'])"
[ "$(uploads)" -gt "$BEFORE" ] || fail "byz-files saw no upload after the retry"
( cd "$WS" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['contentUploaded']==0, r; print('ok: uploaded once, not again')"
( cd "$C" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['contentDownloaded']>=1, r; print('ok: clone downloaded', r['contentDownloaded'])"
grep -q "retry body line" "$C/.gears/artifacts/free--retry-body.md" || fail "clone has not got the body"
( cd "$C" && "$G" sync --json ) | python -c "import json,sys; r=json.load(sys.stdin); assert r['contentUploaded']==0 and r['contentDownloaded']==0, r; print('ok: clone settles: nothing up, nothing down')"

echo "== GRS-56/58: a run through gears-api, evidence checked, pulled by the clone"
SID=$( cd "$WS" && "$G" session new --kind done "backed up for the run" --json | python -c "import json,sys; print(json.load(sys.stdin)['id'])" )
( cd "$WS" && "$G" sync >/dev/null )
RID=$( cd "$WS" && "$G" runbook start gears-migrate --json | python -c "import json,sys; o=json.load(sys.stdin); r=o['run']; assert r['currentStep']=='backup' and r['runbookRef']=='gears-migrate', o; print(r['id'])" )
echo "ok: started $RID"
if ( cd "$WS" && "$G" runbook complete --run "$RID" session:deadbeef >"$WORK/rb.out" 2>&1 ); then cat "$WORK/rb.out"; fail "missing evidence accepted"; fi
grep -q "no session entry" "$WORK/rb.out" || { cat "$WORK/rb.out"; fail "evidence refusal"; }
echo "ok: refused: $(head -1 "$WORK/rb.out")"
( cd "$WS" && "$G" runbook complete --run "$RID" "session:$SID" --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o['stepId']=='backup' and o['currentStep']=='migrate' and o['run']['steps'][0]['evidence'][0]['ref']=='$SID', o; print('ok: backup completed with the session entry')"
( cd "$WS" && "$G" runbook skip --run "$RID" --reason "nothing to migrate" --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o['currentStep']=='verify', o; print('ok: migrate skipped')"
( cd "$WS" && "$G" runbook complete --run "$RID" commit:abc1234 --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o['runStatus']=='completed' and o['notes'], o; print('ok: run completed; note:', o['notes'][0])"
( cd "$C" && "$G" sync >/dev/null && "$G" runbook show "$RID" --json ) | python -c "import json,sys; r=json.load(sys.stdin)['run']; assert r['status']=='completed' and r['version']==4 and r['steps'][1]['skipReason']=='nothing to migrate', r; print('ok: clone pulled the run, v4, completed')"
curl -s -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/workspaces/$WS_ID/runs" | python -c "import json,sys; d=json.load(sys.stdin); assert len(d)==1 and d[0]['id']=='$RID', d; print('ok: GET runs lists it')"

echo "== GRS-59: adopt official runbooks, frozen and editable"
( cd "$WS" && "$G" runbook catalog --json ) | python -c "import json,sys; o=json.load(sys.stdin); f={r['ref']:r['frozen'] for r in o['official']}; assert f=={'single-ticket-development':True,'starter':False}, o; print('ok: catalog', f)"
( cd "$WS" && "$G" runbook adopt single-ticket-development --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o['frozen'] and o['slug']=='single-ticket-development', o; print('ok: adopted frozen')"
if ( cd "$WS" && "$G" runbook edit single-ticket-development --title nope >"$WORK/rb.out" 2>&1 ); then cat "$WORK/rb.out"; fail "frozen copy edited"; fi
grep -q "frozen copy" "$WORK/rb.out" || { cat "$WORK/rb.out"; fail "frozen refusal"; }
if ( cd "$WS" && "$G" runbook step add single-ticket-development "x" >"$WORK/rb.out" 2>&1 ); then cat "$WORK/rb.out"; fail "frozen copy authored"; fi
grep -q "frozen copy" "$WORK/rb.out" || { cat "$WORK/rb.out"; fail "frozen authoring refusal"; }
echo "ok: frozen copy refused: $(head -1 "$WORK/rb.out")"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"title":"x"}' "$GEARS_API/api/v1/workspaces/$WS_ID/runbooks/single-ticket-development")
[ "$code" = "409" ] || fail "gears-api edited a frozen copy ($code)"
( cd "$WS" && "$G" runbook adopt starter --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert not o['frozen'] and o['slug']=='starter-copy', o; print('ok: adopted editable as starter-copy')"
echo '[{"id":"plan","kind":"ask","params":{"question":"goal?"}},{"id":"ship","kind":"do"}]' > "$WORK/steps.json"
( cd "$WS" && "$G" runbook edit starter-copy --title "Our starter" --steps-file "$WORK/steps.json" --json ) | python -c "import json,sys; o=json.load(sys.stdin); assert o['title']=='Our starter' and [s['id'] for s in o['steps']]==['plan','ship'], o; print('ok: editable copy edited')"
( cd "$WS" && "$G" runbook start starter-copy --json ) | python -c "import json,sys; r=json.load(sys.stdin)['run']; assert r['runbook']=='starter-copy' and [s['stepId'] for s in r['steps']]==['plan','ship'], r; print('ok: editable copy ran its own steps inline')"
( cd "$WS" && "$G" runbook start single-ticket-development --json ) | python -c "import json,sys; r=json.load(sys.stdin)['run']; assert r['runbookRef']=='single-ticket-development' and r['currentStep']=='take', r; print('ok: frozen copy ran the official ref')"
( cd "$C" && "$G" sync >/dev/null && "$G" runbook list --json ) | python -c "import json,sys; o=json.load(sys.stdin); a={r['slug']:r['frozen'] for r in o['adopted']}; assert a=={'single-ticket-development':True,'starter-copy':False}, o; print('ok: clone pulled the adopted copies')"

echo "== second sync is a no-op"
# The push window includes the last push's second (stamps are to the
# second), so the first sync after a write in that second re-sends it once.
sleep 1; ( cd "$A" && "$G" sync >/dev/null )
( cd "$A" &&"$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['pushed']==0 and r['failed']==0, r; print('ok: pushed', r['pushed'], 'pulled', r['pulled'])" )
[ -z "$(find "$WS/projects" -name .gears)" ] || fail "a .gears exists under projects/"

echo "== whoami, logout"
( cd "$WS" && "$G" whoami && "$G" logout && ( "$G" whoami | grep -q "not logged in" ) )
echo "E2E OK"
