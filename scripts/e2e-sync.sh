#!/usr/bin/env bash
# End-to-end: gears-api (dev tenant, local Postgres 5441) + two CLI caches in
# one workspace. Proves login, reconcile, push, pull across caches.
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

# Workspace root with one project inside, like ddev/gears/projects/gears-cli.
mkdir -p "$WORK/ws/projects/alpha"
( cd "$WORK/ws" && git init -q -b main . && "$G" init --prefix WS >/dev/null )
( cd "$WORK/ws/projects/alpha" && git init -q -b main . && git remote add origin git@github.com:example/alpha.git && "$G" init >/dev/null )
# A second clone of alpha elsewhere, as another machine would have it.
mkdir -p "$WORK/clone/alpha"
( cd "$WORK/clone/alpha" && git init -q -b main . && git remote add origin https://github.com/example/alpha.git )

echo "== login from the workspace root"
( cd "$WORK/ws" && "$G" login --email a@b.c --password x --label claude-code+local )
echo "== login (reconcile) from project alpha"
( cd "$WORK/ws/projects/alpha" && "$G" login --email a@b.c --password x --label claude-code+local )
grep -q "workspace:" "$WORK/ws/projects/alpha/.gears/project.yaml" || { echo "alpha has no workspace field"; exit 1; }
grep -q "^.gears/$" "$WORK/ws/projects/alpha/.gitignore" || { echo ".gears/ not ignored"; exit 1; }

echo "== write in alpha, sync"
( cd "$WORK/ws/projects/alpha" \
  && "$G" desk send --type handoff --to claude-code+local "Tried A. Result B. Next C." >/dev/null \
  && "$G" session new --kind done "alpha did a thing" >/dev/null \
  && "$G" sync )

echo "== fresh clone binds by remote and pulls"
( cd "$WORK/clone/alpha" && "$G" init >/dev/null && "$G" login --email a@b.c --password x --label grok-build+ubuntu && "$G" sync )
CLONE_ID=$(grep '^id:' "$WORK/clone/alpha/.gears/project.yaml" | awk '{print $2}')
ALPHA_ID=$(grep '^id:' "$WORK/ws/projects/alpha/.gears/project.yaml" | awk '{print $2}')
[ "$CLONE_ID" = "$ALPHA_ID" ] || { echo "clone did not adopt alpha's id ($CLONE_ID vs $ALPHA_ID)"; exit 1; }
( cd "$WORK/clone/alpha" && "$G" -a claude-code+local desk list --json | grep -q "Tried A" ) || { echo "clone did not receive the handoff"; exit 1; }
( cd "$WORK/clone/alpha" && "$G" session list --json | grep -q "alpha did a thing" ) || { echo "clone did not receive the session entry"; exit 1; }

echo "== clone answers; workspace root sees it after sync"
( cd "$WORK/clone/alpha" && "$G" session new --kind decision "clone decided" >/dev/null && "$G" sync >/dev/null )
( cd "$WORK/ws" && "$G" sync >/dev/null && "$G" session list --json | grep -q "clone decided" ) || { echo "root did not receive clone's entry"; exit 1; }

echo "== artifact delete: offline in alpha, pushed on sync, clone drops row and file"
A="$WORK/ws/projects/alpha"; C="$WORK/clone/alpha"; ART=free--doomed-note.md
( cd "$A" && "$G" artifact new free "Doomed note" >/dev/null && "$G" sync >/dev/null )
( cd "$C" && "$G" sync >/dev/null && "$G" artifact list | grep -q "$ART" ) || { echo "clone did not receive the artifact"; exit 1; }
[ -f "$C/.gears/artifacts/$ART" ] || { echo "clone has no file for $ART"; exit 1; }
ART_ID=$(cd "$A" && "$G" artifact list --json | python -c "import json,sys; print([a['id'] for a in json.load(sys.stdin)['artifacts'] if a['file']=='$ART'][0])")
( cd "$A" && "$G" artifact delete "${ART_ID: -8}" )
[ ! -f "$A/.gears/artifacts/$ART" ] || { echo "alpha still has the file"; exit 1; }
( cd "$A" && "$G" artifact reconcile | grep -q "rows and files agree" ) || { echo "alpha reconcile regenerated or adopted"; exit 1; }
( cd "$A" && "$G" artifact list --json | grep -q "$ART" ) && { echo "alpha still lists $ART"; exit 1; }
( cd "$A" && "$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['failed']==0, r; print('ok: delete pushed', r)" )
TOKEN=$(python -c "import json,sys; print(json.load(open(sys.argv[1]))['hosts']['$GEARS_API']['token'])" "$HOME/.gears/credentials.json")
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$GEARS_API/api/v1/artifacts/$ART_ID")
[ "$CODE" = "404" ] || { echo "API still serves the artifact ($CODE)"; exit 1; }
( cd "$C" && "$G" sync >/dev/null )
[ ! -f "$C/.gears/artifacts/$ART" ] || { echo "clone kept the file after the pulled delete"; exit 1; }
( cd "$C" && "$G" artifact reconcile | grep -q "rows and files agree" ) || { echo "clone reconcile regenerated or adopted"; exit 1; }
( cd "$C" && "$G" artifact list --json | grep -q "$ART" ) && { echo "clone still lists $ART"; exit 1; }
echo "ok: row and file gone on both machines; API row soft-deleted"

echo "== second sync is a no-op"
( cd "$WORK/ws/projects/alpha" && "$G" sync --json | python -c "import json,sys; r=json.load(sys.stdin); assert r['pushed']==0 and r['failed']==0, r; print('ok: pushed', r['pushed'], 'pulled', r['pulled'])" )

echo "== whoami, logout"
( cd "$WORK/ws" && "$G" whoami && "$G" logout && ( "$G" whoami | grep -q "not logged in" ) )
echo "E2E OK"
