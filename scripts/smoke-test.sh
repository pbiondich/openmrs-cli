#!/usr/bin/env bash
# Live smoke tests for omrs against the OpenMRS public demo server.
# Usage: ./scripts/smoke-test.sh [path-to-omrs-binary]
# Credentials via env only (never -p on the command line).
set -u

OMRS="${1:-omrs}"
SERVER="https://dev3.openmrs.org/openmrs"
export OMRS_SERVER="$SERVER"
export OMRS_USER="${OMRS_USER:-admin}"
export OMRS_PASSWORD="${OMRS_PASSWORD:-Admin123}"
AUTH=()
PASS=0
FAIL=0

check() {
  local desc="$1"; shift
  if "$OMRS" "$@" >/dev/null 2>&1; then
    echo "PASS: $desc"; PASS=$((PASS + 1))
  else
    echo "FAIL: $desc (exit $?)"; FAIL=$((FAIL + 1))
  fi
}

check_exit() {
  local desc="$1" want="$2"; shift 2
  "$OMRS" "$@" >/dev/null 2>&1
  local got=$?
  if [[ "$got" -eq "$want" ]]; then
    echo "PASS: $desc (exit $got)"; PASS=$((PASS + 1))
  else
    echo "FAIL: $desc (want exit $want, got $got)"; FAIL=$((FAIL + 1))
  fi
}

check_json() {
  local desc="$1"; shift
  if "$OMRS" "$@" --json 2>/dev/null | python3 -c '
import json, sys
d = json.load(sys.stdin)
ok = isinstance(d, dict) and ("results" in d or "uuid" in d or "authenticated" in d)
sys.exit(0 if ok else 1)'; then
    echo "PASS: $desc"; PASS=$((PASS + 1))
  else
    echo "FAIL: $desc"; FAIL=$((FAIL + 1))
  fi
}

echo "=== omrs smoke tests against $SERVER ==="
check      "ping"                       "${AUTH[@]}" ping
check_json "session"                    "${AUTH[@]}" session
check_json "whoami"                     "${AUTH[@]}" whoami
check_exit "whoami unauthenticated"  2  env OMRS_PASSWORD=wrongpass "$OMRS" whoami
check_json "patient search"             "${AUTH[@]}" patient search john --limit 3
check_json "concept search"             "${AUTH[@]}" concept search malaria --limit 3
check_json "location list"              "${AUTH[@]}" location list --limit 5
check_json "user list"                  "${AUTH[@]}" user list --limit 3
check_json "provider list"              "${AUTH[@]}" provider list --limit 3
check_json "get visittype"              "${AUTH[@]}" get visittype
check_json "get encountertype"          "${AUTH[@]}" get encountertype
check_json "get with inline query"      "${AUTH[@]}" get "patient?q=john" --limit 2
check_json "patient search --full"      "${AUTH[@]}" patient search john --limit 2 --full
check_json "concept search --all"       "${AUTH[@]}" concept search "blood pressure" --all
check_json "visit list --since"         "${AUTH[@]}" visit list --since 7d --limit 3

# patient summary: resolve a live uuid first, then check section shape
SUMMARY_UUID=$("$OMRS" "${AUTH[@]}" patient search "mary" --limit 1 --json 2>/dev/null \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['results'][0]['uuid'])" 2>/dev/null || true)
if [[ -n "$SUMMARY_UUID" ]]; then
  if "$OMRS" "${AUTH[@]}" patient summary "$SUMMARY_UUID" --sections problems,meds,allergies --json 2>/dev/null \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
s = d.get("sections", {})
ok = all(k in s and s[k]["status"] in ("ok", "confirmed-none", "none", "unavailable", "withheld") for k in ("problems", "meds", "allergies"))
sys.exit(0 if ok else 1)'; then
    echo "PASS: patient summary"; PASS=$((PASS + 1))
  else
    echo "FAIL: patient summary"; FAIL=$((FAIL + 1))
  fi
else
  echo "FAIL: patient summary (could not resolve test patient)"; FAIL=$((FAIL + 1))
fi
check_exit "summary not-found is exit 4" 4 "${AUTH[@]}" patient summary NO-SUCH-MRN-999
check_exit "ambiguous reference is exit 5" 5 "${AUTH[@]}" patient summary john
check_exit "unknown subcommand is exit 1" 1 patient bogus-subcommand-xyz

# summary counts index present and consistent with sections
if [[ -n "$SUMMARY_UUID" ]] && "$OMRS" "${AUTH[@]}" patient summary "$SUMMARY_UUID" --sections problems,meds --json 2>/dev/null \
  | python3 -c '
import json, sys
d = json.load(sys.stdin)
c = d.get("counts", {})
ok = set(c) == set(d["sections"]) and all(c[k] == len(d["sections"][k]["items"]) for k in c)
sys.exit(0 if ok else 1)'; then
  echo "PASS: summary counts index"; PASS=$((PASS + 1))
else
  echo "FAIL: summary counts index"; FAIL=$((FAIL + 1))
fi
check_exit "bad --since rejected"    1  "${AUTH[@]}" encounter list --since "not-a-date"
check_exit "auth error is exit 2"    2  env OMRS_PASSWORD=wrongpass "$OMRS" patient search john
check_exit "connection error is exit 3" 3 env OMRS_SERVER=https://no-such-host-omrs.invalid/openmrs "$OMRS" ping
check_exit "not-found is exit 4"     4  "${AUTH[@]}" patient get 00000000-dead-beef-0000-000000000000
check_exit "no -p flag (usage)"      1  "$OMRS" --password secret whoami

echo
echo "Results: $PASS passed, $FAIL failed"
exit $((FAIL > 0))
