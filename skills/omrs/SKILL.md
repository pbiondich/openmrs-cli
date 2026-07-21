---
name: omrs
description: >
  Use the omrs CLI to query OpenMRS servers (patients, encounters, obs,
  concepts, visits, locations, clinical summaries) via REST and FHIR2.
  Prefer this skill whenever the user asks to explore an OpenMRS instance,
  look up a patient, pull clinical data, or debug REST/FHIR against OpenMRS.
  Also trigger on: "openmrs", "omrs", "dev3.openmrs.org", "patient summary",
  "OpenMRS REST", "OpenMRS FHIR". Read-only; never invent write/POST flows.
---

# Use `omrs` against OpenMRS

You have a dedicated CLI for OpenMRS. Prefer **`omrs`** over raw `curl` for
REST/FHIR queries: it handles auth profiles, pagination, representation flags,
TTY vs JSON output, and a stable exit/error contract.

If the binary is missing, install then continue:

```bash
go install github.com/pbiondich/openmrs-cli/cmd/omrs@latest
# or from a checkout: go build -o omrs ./cmd/omrs
omrs --help
```

For full contracts when working *in* the openmrs-cli repo, also read
`AGENTS.md` at the repo root. This skill is enough to *use* the tool.

## Bootstrap (do this first)

```bash
# Public sandbox (well-known demo credentials; server resets):
omrs login --demo
omrs whoami

# Real server (password never on argv):
echo "$PASSWORD" | omrs login -s https://host/openmrs -u USER --password-stdin
omrs whoami
```

One-shot without saving a profile:

```bash
export OMRS_SERVER=https://host/openmrs OMRS_USER=admin OMRS_PASSWORD=...
omrs whoami
```

Never use a `-p` / `--password` flag (it does not exist). Never print secrets.

Stored profile passwords are origin-bound. Do **not** expect
`omrs --server https://other-host/openmrs …` to reuse a profile secret for a
different host (that is exit 2 / `AUTH` by design). For another server in one
shot, set `OMRS_SERVER` + `OMRS_USER` + `OMRS_PASSWORD`, or run `omrs login`
for that URL.

Prefer `https://` servers. Remote `http://` is refused unless
`OMRS_ALLOW_INSECURE_HTTP=1`. Prefer env passwords in CI over config-file
password storage.

## Output and error contract

| Stream | Behavior |
|--------|----------|
| stdout | Results. JSON when piped (your usual case); tables on a TTY. Force with `--json` / `--table`. |
| stderr errors | One-line JSON when piped: `{"error":"...","code":"AUTH","httpStatus":401,"detail":"..."}` |
| stderr warnings | `{"warning":"..."}` — no `code`; **do not** treat as failure |

| `code` | exit | meaning |
|--------|------|---------|
| `UNKNOWN` / `USAGE` | 1 | unclassified or bad flags/args |
| `AUTH` | 2 | 401 / not logged in / missing keyring secret |
| `CONNECTION` | 3 | network / timeout |
| `NOT_FOUND` | 4 | 404 / no match |
| `BAD_REQUEST` | 5 | 400, or **ambiguous patient** (candidates in `detail`) |
| `FORBIDDEN` | 6 | 403 authenticated but denied |

On capped `--all` lists, check stdout `"truncated": true` before treating results as complete.

Discover everything with `omrs --help` and `omrs <cmd> --help`. Prefer that over guessing flags.

## Prefer summary over DIY charts

For one patient, **do not** hand-assemble a chart from many calls unless the user asks for a specific slice:

```bash
omrs patient summary <identifier-or-uuid> --json
```

When summary is incomplete, high-stakes, or the site looks nonstandard, escalate:

```bash
omrs patient everything <identifier-or-uuid> --json
```

`everything` is a capped REST composite package (`kind: everything`, compact
typed `e[]` entries). High recall, less clinical filtering than summary.
Read `truncated` and `failed` before treating it as complete. See
`docs/json-output.md`. Not a formal FHIR `$everything` or community standard.

Resolution order: UUID → exact identifier match → unique name. Ambiguous refs exit 5 with candidates in `detail`.

Read JSON in this order:

1. `counts` — per-section item counts; **`null` means the section failed**, not empty.  
2. Each `sections.<name>.status` and `source`.  
3. `partial` / `truncated` flags before claiming completeness.  
4. Item UUIDs for follow-up queries.

| status | Treat as |
|--------|----------|
| `ok` | Data present |
| `confirmed-none` | Record asserts none (e.g. documented no known allergies) |
| `none` | Nothing recorded; **not** proof of clinical absence |
| `unavailable` | Fetch failed |
| `withheld` | Access denied (403) |

Only treat `ok` / `none` as a complete section view when `partial` and `truncated` are absent/false.

Optional: `--sections problems,meds,allergies` to limit work.

## Everyday queries

```bash
omrs patient search "john" --limit 5 --json
omrs patient search --identifier 5574MO-2 --json
omrs concept search "malaria" --json
omrs encounter list --patient <uuid> --since 30d --json
omrs obs list --patient <uuid> --since 2026-01-01 --until yesterday --all --json
omrs visit list --patient <uuid> --since 7d --json
omrs location list --json
omrs get visittype --json
omrs get "patient?q=john" --limit 3 --json
omrs get obs --param patient=<uuid> --param concept=<uuid> --json
```

Useful globals: `--fields uuid,display,person.age` (server-side `v=custom`), `--full`, `--ref`, `--limit`, `--all`, `--profile`, `-s` / `-u`.

Date filters accept ISO dates, relative ages (`7d`, `4w`, `6m`, `1y`), `today`, `yesterday`.  
**Obs** date filtering is client-side after fetch — pair with `--all` or you will under-read.

## Escape hatch

Any REST resource:

```bash
omrs get <path> --param k=v --json
```

`omrs get resource/<uuid>` is a single-record get (no list limit). Collections still honor `--limit` / `--all`.

## Workflow recipes

**Explore a new instance**

1. `omrs ping` then `omrs whoami`  
2. `omrs location list --json`  
3. `omrs patient search <name> --limit 5 --json`  
4. `omrs patient summary <id> --json`  
5. Drill with `encounter list` / `obs list` / `get` using UUIDs from the summary  

**Answer a clinical question about one patient**

1. `patient summary` first  
2. Only then fetch extra obs/encounters if a section was `partial`/`truncated` or the user wants more history  

**Script / CI**

- Prefer env credentials or `login --password-stdin`  
- Always pass `--json` when parsing  
- Branch on exit codes, not string matching stdout  

## Hard rules

1. **Read-only.** Do not invent POST/PUT/DELETE. If the user needs writes, say the tool is query-only today.  
2. **Never put passwords on the command line** or in shell history helpers that expand them into argv.  
3. **Never treat `none` as confirmed clinical absence** unless status is `confirmed-none`.  
4. **Never treat exit 0 + empty list as “patient does not exist”** without checking codes; ambiguity is exit 5.  
5. **Honor `truncated` / `partial` / warnings** before summarizing data as complete.  
6. Prefer `omrs` over ad-hoc HTTP unless the user explicitly wants raw curl or the binary is unavailable and cannot be installed.

## Demo sandbox

- URL: `https://dev3.openmrs.org/openmrs`  
- `omrs login --demo` uses the public admin credential and sets the `demo` profile  
- Data resets; never store real PHI there  
