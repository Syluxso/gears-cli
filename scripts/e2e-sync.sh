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

cleanup() { [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null || true; }
trap cleanup EXIT
fail() { echo "FAIL: $*"; exit 1; }

( cd "$CLI_DIR" && go build -o "$WORK/gears.exe" . )
( cd "$API_DIR" && go build -o "$WORK/gears-api.exe" . )
G="$WORK/gears.exe"

ORG=$("$G" --version >/dev/null; python -c "import uuid;print(uuid.uuid4())")
TEN=$(python -c "import uuid;print(uuid.uuid4())")
GEARS_API_PORT=$PORT PORT=$PORT \
DB_URL='postgres://db:db@127.0.0.1:5441/gears?sslmode=disable' \
GEARS_PAT_SECRET=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
GEARS_DEV_TENANT="$ORG:$TEN" BYZ_KAFKA_ENABLED=false \
"$WORK/gears-api.exe" > "$WORK/api.log" 2>&1 &
API_PID=$!
for i in $(seq 1 30); do curl -sf "$GEARS_API/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "$GEARS_API/healthz" >/dev/null || { echo "api did not start"; cat "$WORK/api.log"; exit 1; }

WS="$WORK/ws"; A="$WS/projects/alpha"; C="$WORK/clone/alpha"
# Workspace root (not itself a repo, like ddev/gears) with one project repo.
mkdir -p "$A" "$C"
( cd "$A" && git init -q -b main . && git remote add origin git@github.com:example/alpha.git )
# A second clone of alpha elsewhere, as another machine would have it.
( cd "$C" && git init -q -b main . && git remote add origin https://github.com/example/alpha.git )
# pid <slug>: a project's id from the root manifest.
pid() { ( cd "$WS" && "$G" projects list --json ) | python -c "import json,sys; print([p['id'] for p in json.load(sys.stdin)['projects'] if p['slug']=='$1'][0])"; }

echo "== init at the workspace root scans projects/ (not logged in yet: offline ids)"
( cd "$WS" && "$G" init --prefix WS )
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
( cd "$C" && "$G" init >/dev/null && "$G" login --email a@b.c --password x --label grok-build+ubuntu && "$G" sync )
CLONE_ID=$(grep '^id:' "$C/.gears/project.yaml" | awk '{print $2}')
[ "$CLONE_ID" = "$ALPHA_ID" ] || fail "clone did not adopt alpha's id ($CLONE_ID vs $ALPHA_ID)"
( cd "$C" && "$G" -a claude-code+local desk list --json | grep -q "Tried A" ) || fail "clone did not receive the handoff"
( cd "$C" && "$G" session list --json ) | python -c "import json,sys; e=[x for x in json.load(sys.stdin)['entries'] if x['text']=='alpha did a thing']; assert len(e)==1 and e[0].get('projectId')=='$ALPHA_ID', e; print('ok: clone has the entry, tagged alpha')"

echo "== clone answers; workspace root sees it after sync, tagged alpha"
( cd "$C" && "$G" session new --kind decision "clone decided" >/dev/null && "$G" sync >/dev/null )
( cd "$WS" && "$G" sync >/dev/null && "$G" session list --json ) | python -c "import json,sys; e=[x for x in json.load(sys.stdin)['entries'] if x['text']=='clone decided']; assert len(e)==1 and e[0].get('projectId')=='$ALPHA_ID', e; print('ok: root has clone entry')"

echo "== artifact delete: made in alpha (root cache), pushed, clone drops row and file"
ART=free--doomed-note.md
( cd "$A" && "$G" artifact new free "Doomed note" --json ) | python -c "import json,sys; a=json.load(sys.stdin); assert a['projectId']=='$ALPHA_ID' and '.gears' in a['path'] and 'projects' not in a['path'].split('.gears')[0][len('$WS'):], a; print('ok: artifact tagged alpha, in the root cache')"
[ -f "$WS/.gears/artifacts/$ART" ] || fail "artifact not in the root cache"
( cd "$A" && "$G" sync >/dev/null )
( cd "$C" && "$G" sync >/dev/null && "$G" artifact list | grep -q "$ART" ) || fail "clone did not receive the artifact"
[ -f "$C/.gears/artifacts/$ART" ] || fail "clone has no file for $ART"
ART_ID=$(cd "$WS" && "$G" artifact list --json | python -c "import json,sys; print([a['id'] for a in json.load(sys.stdin)['artifacts'] if a['file']=='$ART'][0])")
( cd "$A" && "$G" artifact delete "${ART_ID: -8}" )
[ ! -f "$WS/.gears/artifacts/$ART" ] || fail "root still has the file"
( cd "$WS" && "$G" artifact reconcile | grep -q "rows and files agree" ) || fail "root reconcile regenerated or adopted"
( cd "$WS" && "$G" artifact list --json | grep -q "$ART" ) && fail "root still lists $ART"
( cd "$A" && "$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0, r; print('ok: delete pushed')" )
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/artifacts/$ART_ID")
[ "$CODE" = "404" ] || fail "API still serves the artifact ($CODE)"
( cd "$C" && "$G" sync >/dev/null )
[ ! -f "$C/.gears/artifacts/$ART" ] || fail "clone kept the file after the pulled delete"
( cd "$C" && "$G" artifact reconcile | grep -q "rows and files agree" ) || fail "clone reconcile regenerated or adopted"
( cd "$C" && "$G" artifact list --json | grep -q "$ART" ) && fail "clone still lists $ART"
echo "ok: row and file gone on both machines; API row soft-deleted"

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

echo "== second sync is a no-op"
# The push window includes the last push's second (stamps are to the
# second), so the first sync after a write in that second re-sends it once.
sleep 1; ( cd "$A" && "$G" sync >/dev/null )
( cd "$A" &&"$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['pushed']==0 and r['failed']==0, r; print('ok: pushed', r['pushed'], 'pulled', r['pulled'])" )
[ -z "$(find "$WS/projects" -name .gears)" ] || fail "a .gears exists under projects/"

echo "== whoami, logout"
( cd "$WS" && "$G" whoami && "$G" logout && ( "$G" whoami | grep -q "not logged in" ) )
echo "E2E OK"
