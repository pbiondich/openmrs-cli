# openmrs-cli

`omrs` is a command-line tool for querying [OpenMRS](https://openmrs.org) servers through the REST API. It ships as a single binary with no runtime to install, and it renders easy to read tables when you're at a terminal and JSON when you pipe it somewhere else.  Try it out!

The idea behind it is simple: is it possible to create a useful CLI for an OpenMRS implementation that is useful both to humans and AI agents? Structured errors on stderr, stable exit codes, and everything discoverable through `--help` are what make that possible. My sense is that tooling like this is becoming really commonplace for all applications, and I wanted to see what it feels like in practice on OpenMRS.

> This is an experimental, read-only personal project (v0.1)... not an official OpenMRS community tool, at least not yet. :)

## Install

With Go installed:

```bash
go install github.com/pbiondich/openmrs-cli/cmd/omrs@latest
```

Or build from a checkout:

```bash
go build -o omrs ./cmd/omrs && mv omrs /usr/local/bin/
```

## Quick start

```bash
# Log in: prompts for server, username, and password (hidden), validates
# against the server, and stores the password in the OS credential store
# (macOS Keychain / Windows Credential Manager / Secret Service on Linux,
# with a config-file fallback on headless systems)
omrs login

# Check who you are; exits 2 if not authenticated
omrs whoami

# Or configure profiles by hand
omrs config init            # creates 'local' and 'demo' profiles
omrs config use demo        # make the public demo server your default

# Query away
omrs patient search "john"
omrs patient get <uuid>
omrs concept search "malaria"
omrs encounter list --patient <uuid>
omrs obs list --patient <uuid> --concept <uuid>
omrs location list
```

## Full REST API coverage

While the dedicated subcommands cover the common resources, every OpenMRS REST resource is reachable:

```bash
omrs get visittype
omrs get program --limit 50
omrs get patient/<uuid>/encounter
omrs get "patient?q=john"
omrs get obs --param patient=<uuid> --param concept=<uuid>
```

## Output

At a terminal you should get tables as your output. Piped or redirected, you get pretty-printed JSON, so `omrs ... | jq` just works. You can force either mode with `--json` or `--table`.

The CLI's output is yours to control: `--full`, `--ref`, or `--fields uuid,display,person.age` (which maps to the REST API's `v=custom:(...)` representation). Pagination works through `--limit` and `--start`, and `--all` follows the server's pagination links to fetch everything, capped at 5000 so nobody melts a production server by accident.

## Authentication

`omrs login` validates credentials against the server before saving anything. `omrs logout` removes them, including the credential-store entry. `omrs whoami` is a hard auth check that exits 2 when you're not authenticated.

For scripts and agents:

```bash
echo "$OMRS_PW" | omrs login -s http://localhost/openmrs -u admin --password-stdin
```

One deliberate choice that I made that's worth calling out: there are no built-in default credentials. Access requires a profile, environment variables, flags, or a login.

## For AI agents

This tool was designed with agents as first-class users:

- All results go to stdout. All errors go to stderr, and when stderr is piped (the agent case) they're one-line JSON: `{"error":"...","code":"AUTH","httpStatus":401,"detail":"..."}`. Humans at a terminal see plain readable text instead.
- Exit codes are stable: `0` success, `1` unknown, `2` auth, `3` connection/timeout, `4` not found, `5` bad request.
- Every command and flag is discoverable through `--help`, so an agent needs no documentation beyond the binary itself.

The real test is telling your favorite coding agent "use `omrs` to explore my OpenMRS instance" and watching it figure the rest out on its own.

## Configuration

Profiles live in `~/.config/omrs/config.json` (mode 0600):

```json
{
  "defaultProfile": "local",
  "profiles": {
    "local": {"url": "http://localhost/openmrs", "user": "admin", "passwordStore": "keychain"},
    "demo":  {"url": "https://dev3.openmrs.org/openmrs", "user": "admin", "password": "Admin123"}
  }
}
```

Profiles written by `omrs login` carry `"passwordStore": "keychain"` instead of a password. Precedence is flags (`-s/-u/-p`, `--profile`), then env (`OMRS_SERVER`, `OMRS_USER`, `OMRS_PASSWORD`, `OMRS_PROFILE`), then the default profile, then the built-in default URL (`http://localhost/openmrs`, with no default credentials).

## Development

```bash
go build ./... && go vet ./...      # build + lint
./scripts/smoke-test.sh ./omrs      # live smoke tests against dev3.openmrs.org
```

The dependency list is deliberately small (cobra, x/term, go-keyring) and I'd like to keep it that way: a tool that touches clinical systems should be auditable in an afternoon.

## Where this goes next

Write operations behind explicit flags, date filtering (`--since`, `--until`), and a proper GoReleaser pipeline with cross-platform binaries are the likely next steps, roughly in that order.

If you kick the tires and something resonates (or breaks), I'd love to hear about it... open an issue, or find me in the OpenMRS community!