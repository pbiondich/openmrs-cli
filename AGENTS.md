# Agent guide for openmrs-cli

This repo builds `omrs`, a read-only CLI for querying OpenMRS servers over
REST. It is deliberately agent-friendly. This file covers both using the
tool and working on the code.

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
  | `AUTH` | 2 | HTTP 401/403, or not authenticated |
  | `CONNECTION` | 3 | network failure or timeout |
  | `NOT_FOUND` | 4 | HTTP 404 / no match |
  | `BAD_REQUEST` | 5 | HTTP 400, or an ambiguous patient reference (candidates in `detail`) |

- stderr also carries advisory one-line JSON of a second shape:
  `{"warning":"..."}` — no `code` field, exit code unaffected. Warnings
  flag things like client-side filtering, a truncated `--all` fetch, or a
  credential-store problem. Treat them as context, not failure.
- A capped `--all` fetch also sets `"truncated": true` in the stdout
  payload itself — check for it before treating results as complete.

Bootstrapping against a server:

```bash
echo "$PASSWORD" | omrs login -s <server-url> -u <user> --password-stdin
omrs whoami          # verify; exits 2 if not authenticated
```

The dedicated commands (`patient`, `encounter`, `obs`, `concept`, `visit`,
`location`, `user`, `provider`) cover common queries.

For a full clinical picture of one patient, prefer
`omrs patient summary <identifier-or-uuid> --json` over assembling it
yourself: it fans out REST+FHIR queries in parallel and returns
IPS-aligned sections. Any identifier type resolves the patient on an
exact value match (so does a UUID or a unique name); an ambiguous
reference errors with the candidates listed. Read the top-level `counts`
object FIRST — it gives every section's item count up front, so you know
the record's shape before (and regardless of how far) you read the rest.
Each section reports `status` (`ok` | `none` | `unavailable`) and
`source` — treat `none` as "nothing recorded" and `unavailable` as
"could not fetch"; never conflate them. A section may also carry
`partial: true`, meaning a nested fetch failed (the affected item is
marked, e.g. `obsStatus: "unavailable"` on one encounter). UUIDs are
preserved on every item for follow-up queries.

Every other REST resource is reachable through the escape hatch:

```bash
omrs get <path> --param k=v --param k2=v2
```

Useful flags everywhere: `--fields uuid,display,person.age` (server-side
field selection), `--limit N`, `--all` (follows pagination, capped 5000),
`--since`/`--until` on encounter/visit/obs lists (accepts `2026-01-01`,
`7d`, `today`, `yesterday`). Note: obs date filtering is client-side;
pair it with `--all`.

A public sandbox exists at `https://dev3.openmrs.org/openmrs`
(admin / Admin123). It resets periodically; never store anything you
care about there.

## Working on the codebase

Build and verify:

```bash
go build ./... && go vet ./...       # must pass clean
go install ./cmd/omrs                # installs to $GOPATH/bin
./scripts/smoke-test.sh omrs         # live tests against dev3.openmrs.org
```

There are no unit tests; the smoke suite against the live demo server is
the verification standard. Add a smoke check for any new command.

Layout:

- `cmd/omrs/main.go` — entry point only
- `internal/cli/` — one file per command group; `root.go` holds shared flags and the fetch helpers
- `internal/client/` — HTTP, error mapping, pagination; all errors become `client.APIError`
- `internal/config/` — profiles, `~/.config/omrs/config.json`, precedence resolution
- `internal/output/` — TTY detection, table/JSON rendering, error printing
- `internal/secrets/` — OS credential store (go-keyring)

Invariants to preserve (breaking these breaks scripted/agent users):

1. The output contract above: stdout data, stderr one-line JSON errors when piped, stable exit codes.
2. No built-in default credentials, ever.
3. Passwords never in process args, shell history, or plaintext files when a credential store is available.
4. Dependency budget stays minimal (currently cobra, x/term, go-keyring). Justify any addition.
5. v1 is read-only: no POST/PUT/DELETE without an explicit, deliberate design pass.

Releases: push a `v*` tag and GitHub Actions + GoReleaser publish
binaries for macOS (Intel/ARM), Linux (amd64/arm64), and Windows.
