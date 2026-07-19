#!/usr/bin/env bash
# Live smoke tests for omrs against the OpenMRS public demo server.
# Usage: ./scripts/smoke-test.sh [path-to-omrs-binary]
# Credentials via env only (never on the command line).
#
# Compatible with bash 3.2 (macOS default): no empty-array expansion
# under set -u, no associative arrays.
set -u

OMRS="${1:-omrs}"
SERVER="https://dev3.openmrs.org/openmrs"
export OMRS_SERVER="$SERVER"
export OMRS_USER="${OMRS_USER:-admin}"
export OMRS_PASSWORD="${OMRS_PASSWORD:-Admin123}"
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

# Like check_exit but with one KEY=VALUE env override, applied to the
# omrs process only (env prefix must wrap the binary, not become its argv).
check_exit_env() {
  local desc="$1" want="$2" kv="$3"; shift 3
  env "$kv" "$OMRS" "$@" >/dev/null 2>&1
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
check      "ping"                       ping
check_json "session"                    session
check_json "whoami"                     whoami
check_exit_env "whoami unauthenticated" 2 OMRS_PASSWORD=wrongpass whoami
check_json "patient search"             patient search john --limit 3
check_json "concept search"             concept search malaria --limit 3
check_json "location list"              location list --limit 5
check_json "user list"                  user list --limit 3
check_json "provider list"              provider list --limit 3
check_json "get visittype"              get visittype
check_json "get encountertype"          get encountertype
check_json "get with inline query"      get "patient?q=john" --limit 2
check_json "patient search --full"      patient search john --limit 2 --full
check_json "concept search --all"       concept search "blood pressure" --all
check_json "visit list --since"         visit list --since 7d --limit 3

# patient summary: resolve a live uuid first, then check section shape
SUMMARY_UUID=$("$OMRS" patient search "mary" --limit 1 --json 2>/dev/null \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['results'][0]['uuid'])" 2>/dev/null || true)
if [[ -n "$SUMMARY_UUID" ]]; then
  if "$OMRS" patient summary "$SUMMARY_UUID" --sections problems,meds,allergies --json 2>/dev/null \
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

  # single-record fetch honors inline params (v=custom must slim the payload)
  if "$OMRS" get "patient/$SUMMARY_UUID?v=custom:(uuid)" --json 2>/dev/null \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
sys.exit(0 if "uuid" in d and "person" not in d else 1)'; then
    echo "PASS: get instance path honors inline params"; PASS=$((PASS + 1))
  else
    echo "FAIL: get instance path honors inline params"; FAIL=$((FAIL + 1))
  fi

  check_exit "unknown --sections is usage error" 1 patient summary "$SUMMARY_UUID" --sections bogus-section
else
  echo "FAIL: patient summary (could not resolve test patient)"; FAIL=$((FAIL + 1))
fi
check_exit "summary not-found is exit 4" 4 patient summary NO-SUCH-MRN-999
check_exit "ambiguous reference is exit 5" 5 patient summary john
check_exit "unknown subcommand is exit 1" 1 patient bogus-subcommand-xyz

# summary counts: same keys as sections; null when section failed, else len(items)
if [[ -n "$SUMMARY_UUID" ]] && "$OMRS" patient summary "$SUMMARY_UUID" --sections problems,meds --json 2>/dev/null \
  | python3 -c '
import json, sys
d = json.load(sys.stdin)
c = d.get("counts", {})
s = d.get("sections", {})
if set(c) != set(s):
    sys.exit(1)
for k, sec in s.items():
    st = sec.get("status")
    if st in ("unavailable", "withheld"):
        ok = c[k] is None
    else:
        ok = c[k] == len(sec.get("items") or [])
    if not ok:
        sys.exit(1)
sys.exit(0)'; then
  echo "PASS: summary counts index"; PASS=$((PASS + 1))
else
  echo "FAIL: summary counts index"; FAIL=$((FAIL + 1))
fi
check_exit "bad --since rejected"    1  encounter list --since "not-a-date"
check_exit_env "auth error is exit 2" 2 OMRS_PASSWORD=wrongpass patient search john
check_exit_env "connection error is exit 3" 3 OMRS_SERVER=https://no-such-host-omrs.invalid/openmrs ping
check_exit "not-found is exit 4"     4  patient get 00000000-dead-beef-0000-000000000000
check_exit "no -p flag (usage)"      1  --password secret whoami

echo
echo "Results: $PASS passed, $FAIL failed"
exit $((FAIL > 0))
