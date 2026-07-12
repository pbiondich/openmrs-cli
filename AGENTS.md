# Agent guide for openmrs-cli

This repo builds `omrs`, a read-only CLI for querying OpenMRS servers over
REST. It is deliberately agent-friendly. This file covers both using the
tool and working on the code.

## Using omrs as an agent

Everything is discoverable: start with `omrs --help`, then `<command> --help`.

The output contract:

- Results go to stdout. When stdout is piped (your case), output is JSON.
- Errors go to stderr as one-line JSON: `{"error":"...","code":"AUTH","httpStatus":401,"detail":"..."}`
- Exit codes are stable: `0` success, `1` unknown/usage, `2` auth, `3` connection/timeout, `4` not found, `5` bad request.

Bootstrapping against a server:

```bash
echo "$PASSWORD" | omrs login -s <server-url> -u <user> --password-stdin
omrs whoami          # verify; exits 2 if not authenticated
```

The dedicated commands (`patient`, `encounter`, `obs`, `concept`, `visit`,
`location`, `user`, `provider`) cover common queries. Every other REST
resource is reachable through the escape hatch:

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
