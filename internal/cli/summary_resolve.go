package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/output"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// resolvePatient turns an MRN or UUID into a full patient record. An
// ambiguous reference is an error: a clinical summary must never guess.
//
// Order: UUID direct get → REST identifier= (exact ID lookup) → fuzzy
// q= (name / free text). Identifier-first matches patient search
// --identifier and avoids false misses when the right patient is outside
// the top page of a fuzzy search.
func resolvePatient(c *client.Client, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &client.APIError{Message: "empty patient reference", Code: client.CodeBadRequest}
	}
	if uuidRe.MatchString(ref) {
		return c.Get("patient/"+ref, url.Values{"v": {"full"}})
	}

	// OpenMRS identifier= search matches SUBSTRINGS on many versions
	// (identifier=101 also returns 101-6, 1013TS-9, ...), so its results
	// are candidates, not answers: only a structured exact-value match
	// resolves. Partial-only hits fall through to the fuzzy stage and, if
	// nothing better appears, are reported as ambiguity — never as a
	// resolution and never as "not found" while candidates exist.
	idData, err := c.Get("patient", url.Values{
		"identifier": {ref}, "v": {"full"}, "limit": {"25"},
	})
	if err != nil {
		return nil, err
	}
	idResults := asSlice(idData["results"])
	switch exact := exactIdentifierMatches(ref, idResults); len(exact) {
	case 0:
		// fall through to fuzzy search
	case 1:
		return exact[0], nil
	default:
		return nil, ambiguityError(ref, toAnySlice(exact))
	}

	qData, err := c.Get("patient", url.Values{
		"q": {ref}, "v": {"full"}, "limit": {"10"},
	})
	if err != nil {
		return nil, err
	}
	return choosePatient(ref, asSlice(qData["results"]), idResults)
}

// exactIdentifierMatches filters a result page to patients carrying an
// identifier whose structured value equals ref (case-insensitive).
func exactIdentifierMatches(ref string, results []any) []map[string]any {
	var matches []map[string]any
	for _, r := range results {
		rec, _ := r.(map[string]any)
		for _, idAny := range asSlice(rec["identifiers"]) {
			id, _ := idAny.(map[string]any)
			val, _ := id["identifier"].(string)
			if strings.EqualFold(strings.TrimSpace(val), ref) {
				matches = append(matches, rec)
				break
			}
		}
	}
	return matches
}

func toAnySlice(recs []map[string]any) []any {
	out := make([]any, len(recs))
	for i, m := range recs {
		out[i] = m
	}
	return out
}

// choosePatient picks a single patient from a fuzzy q= result page.
// Prefer structured exact identifier matches, then a unique single hit
// as a name convenience. idCandidates are partial identifier= hits kept
// for honest ambiguity reporting when the fuzzy page has nothing better.
func choosePatient(ref string, results []any, idCandidates []any) (map[string]any, error) {
	if len(results) == 0 {
		if len(idCandidates) > 0 {
			return nil, ambiguityError(ref, idCandidates)
		}
		return nil, &client.APIError{
			Message: fmt.Sprintf("no patient found matching %q", ref),
			Code:    client.CodeNotFound,
		}
	}

	if exact := exactIdentifierMatches(ref, results); len(exact) == 1 {
		return exact[0], nil
	} else if len(exact) > 1 {
		return nil, ambiguityError(ref, toAnySlice(exact))
	}

	// No exact identifier match anywhere: a unique fuzzy hit (with no
	// competing identifier candidates) is accepted as a name convenience.
	if len(results) == 1 && len(idCandidates) == 0 {
		rec, _ := results[0].(map[string]any)
		return rec, nil
	}

	// Multiple fuzzy hits, or a single hit shadowed by partial identifier
	// matches, is ambiguity, not absence — report the candidates, never
	// "not found" (which an agent reads as "patient doesn't exist").
	seen := map[string]bool{}
	var cands []any
	for _, r := range append(append([]any{}, results...), idCandidates...) {
		rec, _ := r.(map[string]any)
		u, _ := rec["uuid"].(string)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		cands = append(cands, r)
	}
	return nil, ambiguityError(ref, cands)
}

func ambiguityError(ref string, candidates []any) *client.APIError {
	var cands []string
	for _, r := range candidates {
		rec, _ := r.(map[string]any)
		cands = append(cands, fmt.Sprintf("%s (%s)", output.Extract(rec, "display"), output.Extract(rec, "uuid")))
	}
	return &client.APIError{
		Message: fmt.Sprintf("%q matches %d patients; use a UUID or an exact identifier", ref, len(candidates)),
		Code:    client.CodeBadRequest,
		Detail:  strings.Join(cands, "; "),
	}
}
