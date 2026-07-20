package cli

import (
	"fmt"
	"strings"

	"github.com/pbiondich/openmrs-cli/internal/output"
)

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// renderSummary prints the human panel.
func renderSummary(patient map[string]any, sections map[string]*section) {
	name := output.Extract(patient, "person.display|display")
	fmt.Printf("%s · %s, %sy · DOB %s\n", name,
		output.Extract(patient, "person.gender"),
		output.Extract(patient, "person.age"),
		dateOnly(output.Extract(patient, "person.birthdate")))
	if output.Extract(patient, "person.dead") == "true" {
		fmt.Printf("DECEASED %s\n", dateOnly(output.Extract(patient, "person.deathDate")))
	}
	for _, idAny := range asSlice(patient["identifiers"]) {
		id, _ := idAny.(map[string]any)
		fmt.Printf("ID  %s\n", output.Extract(id, "display"))
	}
	fmt.Printf("uuid %s\n", output.Extract(patient, "uuid"))

	printSection := func(title, name string, line func(map[string]any) string) {
		s, ok := sections[name]
		if !ok {
			return
		}
		fmt.Printf("\n%s\n", title)
		if s.Truncated {
			fmt.Println("  (partial — results may be incomplete; fetch cap reached)")
		} else if s.Partial {
			fmt.Println("  (partial — one or more nested fetches failed)")
		}
		switch s.Status {
		case statusUnavailable:
			fmt.Printf("  (unavailable: %s)\n", s.Error)
		case statusWithheld:
			fmt.Println("  (withheld: the server denied access)")
		case statusConfirmedNone:
			fmt.Println("  No known allergies (confirmed in the record)")
		case statusNone:
			if name == "allergies" {
				fmt.Println("  Not assessed (no allergy information recorded)")
			} else {
				fmt.Println("  (none recorded)")
			}
		default:
			for _, item := range s.Items {
				rec, _ := item.(map[string]any)
				fmt.Printf("  • %s\n", line(rec))
				if name == "encounters" {
					switch rec["obsStatus"] {
					case obsStatusUnavailable:
						fmt.Println("      (observations unavailable — fetch failed)")
						continue
					case obsStatusPartial:
						fmt.Println("      (observations partial — fetch cap reached)")
					}
					for _, o := range asSlice(rec["obs"]) {
						ob, _ := o.(map[string]any)
						fmt.Printf("      - %s\n", output.Extract(ob, "display"))
					}
				}
			}
		}
	}

	printSection("ACTIVE VISIT", "visit", func(r map[string]any) string {
		return fmt.Sprintf("%s · %s · since %s",
			output.Extract(r, "visitType.display"),
			output.Extract(r, "location.display"),
			dateOnly(output.Extract(r, "startDatetime")))
	})
	printSection("PROBLEMS", "problems", func(r map[string]any) string {
		return fmt.Sprintf("%s — %s",
			output.Extract(r, "condition.coded.display|condition.nonCoded|display"),
			strings.ToLower(output.Extract(r, "clinicalStatus")))
	})
	medsTitle := "MEDICATIONS (active)"
	if s := sections["meds"]; s != nil && s.Source == "rest-orders" {
		medsTitle = "MEDICATIONS (active, via REST orders)"
	}
	printSection(medsTitle, "meds", func(r map[string]any) string {
		if med := output.Extract(r, "medicationReference.display|medicationCodeableConcept.text"); med != "" {
			line := med
			if dose := output.Extract(r, "dosageInstruction.text"); dose != "" {
				line += " · " + dose
			}
			if d := output.Extract(r, "authoredOn"); d != "" {
				line += " · since " + dateOnly(d)
			}
			return line
		}
		return fmt.Sprintf("%s · since %s", output.Extract(r, "display"), dateOnly(output.Extract(r, "dateActivated")))
	})
	printSection("ALLERGIES", "allergies", func(r map[string]any) string {
		return output.Extract(r, "display")
	})
	vitalsTitle := "VITALS (latest)"
	if s := sections["vitals"]; s != nil && s.Source == "fhir-uncategorized" {
		vitalsTitle = "VITALS (latest, uncategorized FHIR fallback)"
	}
	printSection(vitalsTitle, "vitals", func(r map[string]any) string {
		return fmt.Sprintf("%s: %s %s (%s)",
			output.Extract(r, "code.text"),
			output.Extract(r, "valueQuantity.value"),
			output.Extract(r, "valueQuantity.unit"),
			dateOnly(output.Extract(r, "effectiveDateTime")))
	})
	printSection(fmt.Sprintf("RECENT ENCOUNTERS (last %d)", summaryEncounters), "encounters", func(r map[string]any) string {
		return fmt.Sprintf("%s  %s · %s",
			dateOnly(output.Extract(r, "encounterDatetime")),
			output.Extract(r, "encounterType.display"),
			output.Extract(r, "location.display"))
	})
	printSection("PROGRAMS", "programs", func(r map[string]any) string {
		line := output.Extract(r, "display")
		if d := output.Extract(r, "dateEnrolled"); d != "" {
			line += " · enrolled " + dateOnly(d)
		}
		if d := output.Extract(r, "dateCompleted"); d != "" {
			line += " · completed " + dateOnly(d)
		}
		return line
	})
}
