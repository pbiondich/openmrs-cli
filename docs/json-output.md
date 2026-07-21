# JSON output design decisions

This note records **why** `omrs` shapes JSON the way it does. It is
documentation for this CLI (and for agents using it). It is **not** an
OpenMRS community standard, FHIR Implementation Guide, or exchange format
other tools are expected to implement.

When in doubt: the live CLI (`--help`, `AGENTS.md`, and the binary) wins
over this file if they ever drift.

---

## Goals

1. **Humans** get readable tables on a TTY.
2. **Scripts and agents** get JSON on stdout when piped (or with `--json`).
3. **Errors** stay on stderr, structured when piped, with stable exit codes
   (see `AGENTS.md`).
4. JSON should be **dense enough for models** (token cost matters) while
   still **typed enough to filter and chain** (not free-text mush).
5. Stay honest about OpenMRS heterogeneity: client-side views are
   best-effort, not a complete clinical record.

Non-goals: inventing a new interop profile for the OpenMRS ecosystem;
emitting full FHIR R4 wire format by default; guaranteeing IPS document
equivalence.

---

## Two patient-level products

| Command | Job | Opinion | Shape |
|---------|-----|---------|--------|
| `patient summary` | Orient: a short clinical picture | Higher (active problems, NKA vs not assessed, vitals category, …) | Sectioned object + absence vocabulary |
| `patient everything` | Site-truth package: what REST could assemble for this patient | Lower (prefer recall over clinical filtering) | Compact typed entry list (below) |

They are **separate verbs** on purpose. Summary is not a thin wrapper
around everything, and everything is not “summary with more sections.”

**Agent habit:** start with `summary`; escalate to `everything` when a
section is incomplete, the question is high-stakes, or the site looks
nonstandard. Do not treat summary `none` as global proof of absence
without considering everything (or targeted list queries).

---

## Why not raw FHIR R4 (or raw OpenMRS REST) by default?

Full FHIR JSON is a strong **system** format and a weak **default LLM
context** format: high structural overhead per clinical fact. Raw OpenMRS
REST is accurate to the server but inconsistent across resources and
noisy for agents (`auditInfo`, nested display objects, pagination
wrappers).

`omrs` therefore uses:

- **Server truth** via REST (and FHIR2 only where a command already does,
  e.g. parts of summary).
- **CLI-shaped JSON** for agent-facing packages — especially `everything`.

That is an encoding choice for this tool, not a rejection of FHIR as an
information model. The compact forms below intentionally **echo FHIR’s
basic resource design** (type, id, code, value, when, references) with
shorter keys and less empty scaffolding.

---

## Compact package shape (`patient everything`)

Default JSON for `everything` is a **patient-scoped package** of typed
entries. Illustrative (not a frozen schema — fields grow as the command
does):

```json
{
  "kind": "everything",
  "patient": "<uuid>",
  "truncated": false,
  "n": {
    "Patient": 1,
    "Encounter": 12,
    "Observation": 80
  },
  "failed": [],
  "e": [
    {
      "t": "Patient",
      "id": "…",
      "name": "Jane Doe",
      "sex": "F",
      "birthDate": "1980-01-01"
    },
    {
      "t": "Encounter",
      "id": "…",
      "when": "2024-03-01T10:00:00",
      "type": "Facility visit"
    },
    {
      "t": "Observation",
      "id": "…",
      "when": "2024-03-01T10:05:00",
      "status": "final",
      "code": { "s": "http://loinc.org", "c": "8480-6", "d": "Systolic BP" },
      "value": { "n": 120, "u": "mmHg" },
      "enc": "…"
    }
  ]
}
```

### Field conventions

| Key | Meaning |
|-----|---------|
| `kind` | Package marker for this CLI (`everything`) |
| `patient` | Subject UUID (package-scoped; not repeated on every entry) |
| `truncated` | At least one type hit a fetch cap or left pages unread |
| `n` | Counts by entry type (`t`) after successful fetches |
| `failed` | Types that error’d (`t`, message, optional `code`) — omit or `[]` if none |
| `e` | Entries |
| `t` | Type name aligned with FHIR resource names where practical (`Patient`, `Observation`, …) |
| `id` | Resource id / UUID when known |
| `when` | Primary datetime for the fact |
| `code` | Concept: optional `s` system, `c` code, `d` display |
| `value` | Tagged value: quantity `{n,u}`, coding `{code:{…}}`, string `{s:"…"}`, bool `{b:true}` |
| `enc` | Encounter id reference (short), when relevant |
| `status` | Status string when the source has one |

**Omit empty fields.** Do not emit nulls or empty arrays for optional
slots just to mirror FHIR cardinality.

### Source of data

`everything` is a **REST composite**: OpenMRS REST is fetched and projected
into the package. It is shaped *like* a FHIR `$everything` result for
familiarity (typed bag of resources for one patient), but it is **not**
a claim of conformance to FHIR `$everything`, IPS, or any IG.

Caps apply per type (and may be flagged globally). Hitting a cap sets
`truncated: true`. Callers that need more history should narrow with
future filters or use dedicated list commands with `--all`.

### What this is not

- Not a community “OpenMRS compact FHIR” convention  
- Not guaranteed stable across major CLI versions without a note in
  release notes (we will try not to break agents casually)  
- Not a substitute for server-side privilege rules or audit logs  

---

## `patient summary` JSON (existing)

Summary stays **section-oriented**, because absence and partiality are
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

- Section `status`: `ok` | `confirmed-none` | `none` | `unavailable` | `withheld`
  (partiality is `partial` / `truncated` flags, not a sixth status string).
- `counts` uses **`null` for failed sections**, `0` for empty success — so
  agents do not confuse “fetch failed” with “nothing recorded.”
- `confirmed-none` is meaningful where the record asserts absence (today:
  allergies / NKA); do not invent it for every empty list.
- Exit code `0` means the command produced a summary payload after
  resolving the patient — **not** “every section is complete clinical
  truth.” Read section flags.

Summary may still use FHIR2 for some sections (e.g. meds/vitals) with REST
fallback; that is an implementation detail of those sections, not a
promise that summary is a FHIR document.

---

## List / get commands

Most list and get commands pass through OpenMRS REST JSON (plus CLI
pagination flags). They are **not** forced through the compact `e[]`
package. That keeps the escape hatch (`omrs get`) predictable for
debugging and one-off queries.

Compact packaging is for **patient-level assemble** commands
(`everything`, and the section model for `summary`), not for every row of
`obs list`.

---

## Errors and warnings

Unchanged contract:

- **stdout** — successful data only  
- **stderr** — errors (and one-line JSON when piped); warnings as
  `{"warning":"…"}` without a `code` field  
- **exit codes** — stable; see `AGENTS.md`  

Security-related resolution failures (missing keyring secret, credential
origin mismatch, etc.) use `AUTH` / exit 2 and do not send credentials to
the wrong host.

---

## Token efficiency (why compact keys)

Short keys (`t`, `e`, `n`, `code.d`) and omitted empties reduce tokens for
agent context windows. The tradeoff is readability for humans — hence
tables on a TTY and this design note for authors.

If a future flag expands to verbose FHIR R4 or raw REST (`--wire=…`),
that will be explicit and non-default for agent skill paths.

---

## Evolution

Changes that break agent parsers (renaming `e`, changing absence
semantics, removing `truncated`) should show up in release notes.

Additive fields and new entry types are preferred over silent renames.

This document should be updated when the compact package fields change in a
breaking way, or when summary’s top-level contract gains an explicit
completeness rollup — not for every internal refactor.

`patient everything` shipped with the compact package described above
(default JSON). Caps are CLI flags (`--cap-obs`, etc.).

---

## Related files

| File | Role |
|------|------|
| `AGENTS.md` | Full agent/runtime contract (exit codes, summary status) |
| `skills/omrs/SKILL.md` | Portable skill for coding agents |
| `README.md` | Human install and quickstart |
