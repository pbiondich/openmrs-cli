# openmrs-cli

`omrs` is a command-line tool for querying [OpenMRS](https://openmrs.org) servers through the REST API. It ships as a single binary with no runtime to install, and it renders easy to read tables when you're at a terminal and JSON when you pipe it somewhere else.  Try it out!

The idea behind it is simple: is it possible to create a useful CLI for an OpenMRS implementation both for humans and AI agents? Structured errors on stderr, stable exit codes, and everything discoverable through `--help` are what make that possible. My sense is that tooling like this is becoming really commonplace for all applications, and I wanted to see what it feels like in practice on OpenMRS.

> This is an experimental, read-only personal project... not an official OpenMRS community tool, at least not yet. :)

## Install

One line, nothing else required (macOS and Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/pbiondich/openmrs-cli/main/install.sh | sh
```

That detects your platform, downloads the latest release, verifies its
checksum, and puts `omrs` on your PATH. I watched someone try to install
this the hard way... they had no Go, no Homebrew, and the binary wasn't on
their PATH, and each of those is a place to give up. Now it's one command.
(If piping curl to sh isn't your style, [read the script first](install.sh)...
it's short, and it only ever downloads from this repo's releases.)

On Windows, grab the `.zip` from the [releases page](https://github.com/pbiondich/openmrs-cli/releases).

If you have Go and prefer it:

```bash
go install github.com/pbiondich/openmrs-cli/cmd/omrs@latest
```

Or build from a checkout:

```bash
go build -o omrs ./cmd/omrs && mv omrs /usr/local/bin/
```

## Quick start

Public sandbox:

```bash
omrs login --demo
omrs whoami
omrs patient search "john"
```

Your own server (or localhost):

```bash
omrs login -s https://your-openmrs.example.org/openmrs -u youruser
# or: omrs login -s http://localhost/openmrs -u admin
omrs whoami
omrs patient summary <id>
omrs patient everything <id> --json   # compact REST package (see docs/json-output.md)
omrs concept search "malaria"
omrs encounter list --patient <uuid> --since 30d
omrs obs list --patient <uuid> --since 2026-01-01 --until yesterday --all
omrs location list
```

Details on where passwords go and how profiles work are under [Authentication](#authentication) and [Configuration](#configuration) below.

Date filters accept ISO dates (`2026-01-01`), relative ages (`7d`, `4w`, `6m`, `1y`), and `today`/`yesterday`. Encounters and visits filter on the server; obs filters client-side after fetch (the REST API ignores date parameters there), so pair it with `--all` for complete results... the CLI will remind you if you forget.

## Patient summaries

`omrs patient summary <identifier-or-uuid>` assembles a one-page clinical picture from parallel REST and FHIR queries: active visit, problems, medications, allergies, vitals, recent encounters with their observations, and program enrollments. The sections follow the [International Patient Summary (IPS)](https://hl7.org/fhir/uv/ips/) core where it applies, so the output will hopefully feel familiar and maps cleanly onto `$summary` when OpenMRS grows IPS server-side support.

`omrs patient everything <identifier-or-uuid>` is the companion when the details of a patient record matter. OpenMRS implementations often become bespoke, so a curated summary will always be best-effort... `everything` is a capped REST dump shaped for tools and agents (compact typed JSON, not a full FHIR wire dump), so you can compare the summary to more of what the server actually has. Different job than `summary`; same patient resolution. Details in [`docs/json-output.md`](docs/json-output.md).

Any identifier type resolves the patient, not just the MRN... an old ID, a national ID, whatever the server knows them by, as long as the value matches exactly. A unique name works too, as a convenience. And an ambiguous identifier always returns an error, with the candidates you might have meant instead.

```bash
omrs patient summary 5574MO-2
omrs patient summary 5574MO-2 --sections problems,meds,allergies --json
omrs patient everything 5574MO-2 --json
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

If you want to test the CLI against the OpenMRS community demo site, just use `omrs login --demo`. It uses the well known credential for the [dev3.openmrs.org](https://dev3.openmrs.org/openmrs) server.

For everything else, `omrs login -s <url> -u <user>` prompts for a password (hidden), checks it against the server *before* saving, and stores it in the OS credential store (Keychain / Credential Manager / Secret Service)... with a config-file fallback only when there's no keyring. `omrs logout` clears the stored secret. `omrs whoami` is the hard check (exit 2 if you're not authenticated).

Scripts and agents:

```bash
echo "$OMRS_PW" | omrs login -s http://localhost/openmrs -u admin --password-stdin
# one-shot without saving a profile:
#   OMRS_SERVER=... OMRS_USER=... OMRS_PASSWORD=... omrs whoami
```

### Credential store notes

Passwords never appear on the command line (there is no `-p` flag). Prefer the OS store when it is available; `OMRS_PASSWORD` and `--password-stdin` cover headless CI and agents.

**Origin binding:** a password saved for a profile is only sent to that profile’s server origin (scheme + host + port). `omrs --server https://other.example/openmrs …` does *not* reuse the profile secret for a different host (exit 2 / `AUTH`). For one-shot use against another server, set `OMRS_PASSWORD` (and usually `OMRS_SERVER` / `OMRS_USER`) for that process, or run `omrs login` for a profile aimed at the new URL. Changing a profile’s URL to a different origin clears its stored credentials.

**HTTPS:** cleartext `http://` is allowed only for loopback (`localhost` / `127.0.0.1` / `::1`). Remote HTTP is refused unless you pass `--allow-insecure-http` or set `OMRS_ALLOW_INSECURE_HTTP=1` (lab networks only; a warning is still printed).

**Where passwords live:** prefer the OS credential store. If it is unavailable, login does **not** write the password into `config.json` unless you opt in with `--store-password-in-config` or `OMRS_ALLOW_CONFIG_PASSWORD=1`. Headless CI should use `OMRS_PASSWORD` instead of a saved profile secret.

One library-level caveat on macOS: [go-keyring](https://github.com/zalando/go-keyring) stores secrets via the `security` CLI rather than the Keychain API with an ACL prompt. In practice, **any process running as your user can often read the stored password without a Keychain unlock dialog**. That is weaker than the “app-bound Keychain item” model people sometimes assume. Linux Secret Service and Windows Credential Manager have their own trust models too. Treat a logged-in shell as trusted, use short-lived env credentials when you can, and `omrs logout` when you are done with a profile.

## For AI agents

This tool was designed with agents as first-class users:

- All results go to stdout. All errors go to stderr, and when stderr is piped (the agent case) they're one-line JSON: `{"error":"...","code":"AUTH","httpStatus":401,"detail":"..."}`. Humans at a terminal see plain readable text instead.
- Exit codes are stable: `0` success, `1` unknown, `2` auth, `3` connection/timeout, `4` not found, `5` bad request, `6` forbidden (HTTP 403).
- Every command and flag is discoverable through `--help`, so an agent needs no documentation beyond the binary itself.

The real test is telling your favorite coding agent "use `omrs` to explore my OpenMRS instance" and watching it figure the rest out on its own.

## Configuration

Profiles live in `~/.config/omrs/config.json` (mode 0600). `omrs config init` seeds empty `local` and `demo` shells (URL/user only). After login, a profile looks more like:

```json
{
  "defaultProfile": "demo",
  "profiles": {
    "local": {"url": "http://localhost/openmrs", "user": "admin", "passwordStore": "keychain"},
    "demo":  {"url": "https://dev3.openmrs.org/openmrs", "user": "admin", "passwordStore": "keychain"}
  }
}
```

`passwordStore: "keychain"` means the secret is in the OS store, not this file. Switch defaults with `omrs config use <name>`, or pass `--profile` / `OMRS_PROFILE` for one command. Connection settings resolve as: flags → env (`OMRS_SERVER`, `OMRS_USER`, `OMRS_PASSWORD`, `OMRS_PROFILE`) → default profile → built-in URL `http://localhost/openmrs` (still no default password).

## Development

```bash
go build ./... && go vet ./... && go test ./...   # build + unit tests
./scripts/smoke-test.sh ./omrs                    # live smoke against the public demo
```

The dependency list is deliberately small (cobra, x/term, go-keyring) and I'd like to keep it that way: a tool that touches patient data should be auditable in an afternoon.

Agent and contributor contracts live in [`AGENTS.md`](AGENTS.md). A portable skill for coding agents that *use* `omrs` against a server is in [`skills/omrs/SKILL.md`](skills/omrs/SKILL.md). Design notes for JSON shapes (summary vs everything, compact entries) are in [`docs/json-output.md`](docs/json-output.md).

## License

[Mozilla Public License 2.0](https://www.mozilla.org/en-US/MPL/2.0/) with the OpenMRS healthcare disclaimer appended (see [`LICENSE`](LICENSE)). GitHub’s license classifier may show this as “Other” / `NOASSERTION` because the file is not pure stock MPL-2.0 text; the governing terms are still MPL-2.0 plus that healthcare disclaimer.

## Where this goes next

Write operations behind explicit flags and more convenience query commands as requested and that I can dream up. :)

If you kick the tires and something resonates (or breaks), I'd love to hear about it... open an issue, or find me in the OpenMRS community!
