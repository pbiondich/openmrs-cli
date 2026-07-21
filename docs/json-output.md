# JSON output design decisions

This note records *why* `omrs` shapes JSON the way it does. It is
documentation for this CLI (and for agents using it)... not an OpenMRS
community standard, not a FHIR Implementation Guide, and not an exchange
format I expect other tools to implement.

When in doubt: the live CLI (`--help`, `AGENTS.md`, and the binary) wins
over this file if they ever drift.

---

## Goals

1. Humans get readable tables on a TTY.
2. Scripts and agents get JSON on stdout when piped (or with `--json`).
3. Errors stay on stderr, structured when piped, with stable exit codes
   (see `AGENTS.md`).
4. JSON should be dense enough for models (token cost matters) while
   still typed enough to filter and chain... not free-text mush.
5. Stay honest about OpenMRS heterogeneity: client-side views are
   best-effort, not a complete clinical record.

Non-goals: inventing a new interop profile for the OpenMRS ecosystem,
emitting full FHIR R4 wire format by default, or guaranteeing IPS
document equivalence.

---

## Two patient-level products

| Command | Job | Opinion | Shape |
|---------|-----|---------|--------|
| `patient summary` | Orient: a short clinical picture | Higher (active problems, NKA vs not assessed, vitals category, ...) | Sectioned object + absence vocabulary |
| `patient everything` | Site-truth package: what REST could assemble for this patient | Lower (prefer recall over clinical filtering) | Compact typed entry list (below) |

They are separate verbs on purpose. Summary is not a thin wrapper around
everything, and everything is not "summary with more sections."

The agent habit I want to encourage: start with `summary`, and escalate
to `everything` when a section is incomplete, the question is
high-stakes, or the site looks nonstandard. Do not treat a summary
`none` as global proof of absence without considering everything (or a
targeted list query).

---

## Why not raw FHIR R4 (or raw OpenMRS REST) by default?

Full FHIR JSON is a strong system format and a weak default LLM context
format: high structural overhead per clinical fact. Raw OpenMRS REST is
accurate to the server but inconsistent across resources and noisy for
agents (`auditInfo`, nested display objects, pagination wrappers).

So `omrs` uses server truth via REST (and FHIR2 only where a command
already does, e.g. parts of summary), projected into CLI-shaped JSON for
the agent-facing packages... especially `everything`. That is an
encoding choice for this tool, not a rejection of FHIR as an information
model. The compact forms below intentionally echo FHIR's basic resource
design (type, id, code, value, when, references) with shorter keys and
less empty scaffolding.

---

## Compact package shape (`patient everything`)

Default JSON for `everything` is a patient-scoped package of typed
entries. Every field below is one the CLI actually emits today:

```json
{
  "kind": "everything",
  "patient": "<uuid>",
  "n": {
    "Patient": 1,
    "Visit": 4,
    "Encounter": 12,
    "Observation": 80
  },
  "e": [
    {
      "t": "Patient",
      "id": "…",
      "name": "Jane Doe",
      "sex": "F",
      "birthDate": "1980-01-01"
    },
    {
      "t": "Visit",
      "id": "…",
      "type": "OPD Visit",
      "location": "Ward A",
      "start": "2026-02-03T00:00:00.000+0000",
      "end": "2026-02-03T00:00:00.000+0000"
    },
    {
      "t": "Encounter",
      "id": "…",
      "when": "2026-02-03T10:00:00.000+0000",
      "type": "Adult Visit",
      "visit": "<visit-uuid>"
    },
    {
      "t": "Observation",
      "id": "…",
      "when": "2026-02-03T10:05:00.000+0000",
      "code": { "s": "https://openmrs.org/concept", "c": "<concept-uuid>", "d": "Weight (kg)" },
      "value": { "n": 46, "u": "kg" },
      "enc": "…"
    }
  ]
}
```

`truncated: true` appears at the top level when any type hit a fetch
cap with rows actually left behind; `failed` lists types whose fetch
errored. Both are omitted when clean.

### Field conventions

| Key | Meaning |
|-----|---------|
| `kind` | Package marker for this CLI (`everything`) |
| `patient` | Subject UUID (package-scoped; not repeated on every entry) |
| `truncated` | At least one type left rows behind (cap overshoot or unread pages). Reaching a cap exactly, with nothing further, does not set it |
| `n` | Counts by entry type after successful fetches; `0` means fetched and empty. Failed types are absent from `n` and appear in `failed` instead |
| `failed` | Types that errored: `t`, message, and the error `code` / `httpStatus` when known |
| `e` | Entries, ordered by type, then chronologically (`when`), then id |
| `t` | Type name, aligned with FHIR resource names where practical |
| `id` | Resource UUID when known |
| `when` | Primary *clinical* datetime for the fact (onset, activation, observation time) |
| `recorded` | Record-creation time, used only when no clinical datetime exists. The two are different facts, and I don't want audit metadata masquerading as onset |
| `code` | Concept: `s` system, `c` code, `d` display. Today `s` is always `https://openmrs.org/concept` with the concept UUID as `c`... external mappings (LOINC, SNOMED) may join later |
| `value` | Tagged value: quantity `{n, u}` (units from the concept; `u` omitted when the dictionary has none), coded `{code:{…}}`, string `{s}`, bool `{b}`. Text values are never coerced to numbers, even when they look numeric |
| `enc` | Encounter UUID reference, when relevant |
| `visit` | Visit UUID reference on Encounter entries, when the server links them |
| `status` | Status string when the source has one (e.g. Condition clinical status, order action) |

Omit empty fields. No nulls or empty arrays just to mirror FHIR
cardinality.

### One deliberate divergence from FHIR: `Visit`

FHIR maps both OpenMRS visits and encounters onto its `Encounter`
resource (the fhir2 module does exactly that, with `partOf`). This
package does not: the gap is not squeamishness about FHIR, it is that
**everything reports site truth, and the OpenMRS data model has two
distinct things**. The visit is the container ("the patient was at the
facility Tuesday"); the encounter is the clinical interaction inside it.
Merging them is an interop transformation, which is precisely the kind
of opinion this command promised not to have. So `Visit` is its own
entry type, and Encounter entries reference their visit. Pre-visit-era
records (encounters with no visit on the server) simply carry no `visit`
key... which the merged model could not have represented honestly.

If a future `--wire=fhir` mode emits real FHIR R4, the merge to
`Encounter` + `partOf` belongs there, matching fhir2's mapping.

### Caps and flags

Caps apply per type (`--cap-obs`, `--cap-visit`, ...). The global
list-shaping flags (`--limit`, `--all`, `--fields`, `--full`, `--ref`,
`--start`) are rejected with a `USAGE` error rather than silently
ignored: a flag that looks obeyed and isn't is how agents get misled.

### What this is not

- Not a community "OpenMRS compact FHIR" convention.
- Not guaranteed stable across major CLI versions without a note in the
  release notes (I will try not to break agents casually).
- Not a substitute for server-side privilege rules or audit logs.

---

## `patient summary` JSON (existing)

Summary stays section-oriented, because absence and partiality are
first-class clinical concerns for a curated view:

```json
{
  "counts": { "problems": 1, "allergies": 0, "meds": null },
  "patient": { },
  "sections": {
    "problems": {
      "status": "ok",
      "source": "rest",
      "items": [ ]
    }
  }
}
```

Design points (see also `AGENTS.md`):

- Section `status` is `ok` | `confirmed-none` | `none` | `unavailable` |
  `withheld`; partiality is the `partial` / `truncated` flags, not a
  sixth status string.
- `counts` uses `null` for failed sections and `0` for empty success, so
  agents never confuse "fetch failed" with "nothing recorded." (Note the
  sibling difference: everything drops failed types from `n` and lists
  them in `failed`. Summary is a fixed set of sections, so null-per-key
  fits; everything is an open bag of types, so absence-plus-`failed`
  fits. Same honesty, two shapes.)
- `confirmed-none` is meaningful where the record asserts absence
  (today: allergies / NKA). Do not invent it for every empty list.
- Exit code `0` means the command produced a payload after resolving the
  patient... not "every section is complete clinical truth." Read the
  section flags.

Summary may still use FHIR2 for some sections (meds, vitals) with REST
fallback; that is an implementation detail of those sections, not a
promise that summary is a FHIR document.

---

## List / get commands

Most list and get commands pass through OpenMRS REST JSON (plus CLI
pagination flags). They are not forced through the compact `e[]`
package... that keeps the escape hatch (`omrs get`) predictable for
debugging and one-off queries. Compact packaging is for the
patient-level assemble commands only.

---

## Errors and warnings

Unchanged contract: stdout carries successful data only; stderr carries
errors (one-line JSON when piped) and `{"warning":"…"}` advisories
without a `code` field; exit codes are stable per `AGENTS.md`.

Security-related resolution failures (missing keyring secret, credential
origin mismatch) use `AUTH` / exit 2 and never send credentials to the
wrong host.

---

## Token efficiency (why compact keys)

Short keys (`t`, `e`, `n`, `code.d`) and omitted empties reduce tokens
for agent context windows. The tradeoff is readability for humans...
hence tables on a TTY, and this note for authors.

---

## Evolution

Changes that would break agent parsers (renaming `e`, changing absence
semantics, removing `truncated`) should show up in release notes.
Additive fields and new entry types are preferred over silent renames.
Update this document when the compact package changes in a breaking way,
not for every internal refactor.

If something here doesn't match what the binary does, trust the binary
and tell me... that is a bug in this file.

---

## Related files

| File | Role |
|------|------|
| `AGENTS.md` | Full agent/runtime contract (exit codes, summary status) |
| `skills/omrs/SKILL.md` | Portable skill for coding agents |
| `README.md` | Human install and quickstart |
