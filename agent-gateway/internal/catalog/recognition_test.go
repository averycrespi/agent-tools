package catalog

import "testing"

func TestInventoryRecognitionNamesAndLiteralIDs(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, query := range []string{"", "Reporting", "reportng", "reproting", "REPORTING server", id, "G5FAV"} {
		if !MatchInventoryIdentity("Reporting server", id, query) {
			t.Errorf("expected match for %q", query)
		}
	}
	for _, query := range []string{"Rpe", "report123", "reporting absent", "g5fav", "G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAW"} {
		if MatchInventoryIdentity("Reporting server", id, query) {
			t.Errorf("unexpected match for %q", query)
		}
	}
}
