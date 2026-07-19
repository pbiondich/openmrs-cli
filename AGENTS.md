# Agent guide for openmrs-cli

This repo builds `omrs`, a read-only CLI for querying OpenMRS servers over
REST. It was designed with agents as first-class users, and this file
covers both using the tool and working on the code.

## Using omrs as an agent

Everything is discoverable: start with `omrs --help`, then `<command> --help`.

The output contract:

- Results go to stdout. When stdout is piped (your case), output is JSON.
- Errors go to stderr as one-line JSON: `{"error":"...","code":"...","httpStatus":...,"detail":"..."}`
- The `code` literals and their exit codes:

  | code | exit | meaning |
  |------|------|---------|
  | `UNKNOWN` | 1 | unclassified failure |
  | `USAGE` | 1 | bad command, flag, or arguments |
  | `AUTH` | 2 | HTTP 401, not authenticated, or missing credential-store secret |
  | `CONNECTION` | 3 | network failure or timeout |
  | `NOT_FOUND` | 4 | HTTP 404 / no match |
  | `BAD_REQUEST` | 5 | HTTP 400, or an ambiguous patient reference (candidates in `detail`) |
  | `FORBIDDEN` | 6 | HTTP 403 — authenticated but access denied |

- stderr also carries advisory one-line JSON of a second shape,
  `{"warning":"..."}`: no `code` field, and the exit code is unaffected.
  Warnings flag things like client-side filtering or a truncated `--all`
  fetch. Treat them as context, not failure. A missing keychain secret is
  a hard `AUTH` error (exit 2), not a warning.
- A capped `--all` fetch also sets `"truncated": true` in the stdout
  payload itself... check for it before treating results as complete.

Bootstrapping against a server:

```bash
# Public sandbox (no prompts):
omrs login --demo
omrs whoami

# Production / private server:
echo "$PASSWORD" | omrs login -s <server-url> -u <user> --password-stdin
omrs whoami          # verify; exits 2 if not authenticated

# Or without login: OMRS_SERVER / OMRS_USER / OMRS_PASSWORD (never -p)
```

The dedicated commands (`patient`, `encounter`, `obs`, `concept`, `visit`,
`location`, `user`, `provider`) cover common queries.

For a full clinical picture of one patient, prefer
`omrs patient summary <identifier-or-uuid> --json` over assembling it
yourself: it fans out REST and FHIR queries in parallel and returns
IPS-aligned sections. Resolution order is UUID, then exact identifier=
lookup, then fuzzy name search; an ambiguous reference errors with the
candidates listed. Read the top-level `counts`
object first: it gives every section's item count up front, so you know
the record's shape before (and regardless of how far) you read the rest.
Each section reports `status` and `source`. The status vocabulary
follows a six-state absence model, cross-checked against FHIR's
[`Composition.section.emptyReason`](https://hl7.org/fhir/R4/valueset-list-empty-reason.html)
codes and the related
[`dataAbsentReason`](https://hl7.org/fhir/R4/valueset-data-absent-reason.html)
value set:

  | status | meaning |
  |--------|---------|
  | `ok` | data present |
  | `confirmed-none` | the record asserts none exists (e.g. a documented "No known allergies") |
  | `none` | nothing recorded; no assertion was ever made |
  | `unavailable` | the fetch failed |
  | `withheld` | the server denied access (HTTP 403) |

Treat `none` as "nobody ever asked", never as proof of absence... only
`confirmed-none` asserts absence. A section may also carry
`partial: true`, meaning a nested fetch failed; the affected item is
marked, e.g. `obsStatus: "unavailable"` on one encounter. UUIDs are
preserved on every item for follow-up queries.

One honest limit worth knowing: a server that silently filters rows for
privacy reasons is invisible to any client vocabulary. `withheld` only
appears when the server says so explicitly.

Every other REST resource is reachable through the escape hatch:

```bash
omrs get <path> --param k=v --param k2=v2
```

Useful flags everywhere: `--fields uuid,display,person.age` (server-side
field selection), `--limit N`, `--all` (follows pagination, capped 5000),
`--since`/`--until` on encounter/visit/obs lists (accepts `2026-01-01`,
`7d`, `today`, `yesterday`). Note: obs date filtering is client-side, so
pair it with `--all`.

A public sandbox exists at `https://dev3.openmrs.org/openmrs`
(admin / Admin123). `omrs login --demo` is the one-command on-ramp: it
uses those well-known credentials, saves them under the `demo` profile
(and makes it the default), and prints a reminder that the server resets.
Never store anything you care about there.

## Working on the codebase

Build and verify:

```bash
go build ./... && go vet ./... && go test ./...   # must pass clean
go install ./cmd/omrs                             # installs to $GOPATH/bin
./scripts/smoke-test.sh omrs                      # live tests against dev3.openmrs.org
```

Unit tests cover pure logic (dates, errors, pagination safety, patient
resolve, config resolution). The smoke suite against the live demo server
is the integration bar. Add a unit test for pure helpers and a smoke check
for any new command.

Layout:

- `cmd/omrs/main.go` is the entry point, and nothing more.
- `internal/cli/` holds one file per command group; `root.go` has the shared flags and fetch helpers.
- `internal/client/` is HTTP, error mapping, and pagination; all errors become `client.APIError`.
- `internal/config/` manages profiles, `~/.config/omrs/config.json`, and precedence resolution.
- `internal/output/` does TTY detection, table/JSON rendering, and error printing.
- `internal/secrets/` wraps the OS credential store (go-keyring).

Invariants to preserve (breaking these breaks scripted and agent users):

1. The output contract above: stdout data, stderr one-line JSON errors when piped, stable exit codes.
2. No built-in default credentials, ever.
3. Passwords never in process args, shell history, or plaintext files when a credential store is available.
4. The dependency budget stays minimal (currently cobra, x/term, go-keyring). Justify any addition.
5. v1 is read-only: no POST/PUT/DELETE without an explicit, deliberate design pass.

Releases: push a `v*` tag and GitHub Actions + GoReleaser publish
binaries for macOS (Intel/ARM), Linux (amd64/arm64), and Windows.
