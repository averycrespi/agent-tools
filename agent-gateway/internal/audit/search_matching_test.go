package audit

import "testing"

func TestAuditNameSearchBounds(t *testing.T) {
	for _, test := range []struct {
		name, query string
		want        bool
	}{
		{"Café Workshop", "CAFE workshpo", true},
		{"Workshop", "workshop", true},
		{"Workshop", "workshp", true},
		{"Workshop", "worksshopp", false},
		{"Alpha 1234", "1235", false},
		{"Cat", "bat", false},
		{"Workshop", "work shp", false},
		{"Workshop Library", "library workshpo", true},
		{"Other workshop", "missing workshop", false},
		{"Workshop", "   ", false},
		{"Literal % name", "%", true},
		{"Workshop", "%", false},
		{"Ａlpha", "alpha", true},
	} {
		t.Run(test.name+"/"+test.query, func(t *testing.T) {
			if got := auditNameMatches(test.name, test.query); got != test.want {
				t.Fatalf("match = %v, want %v", got, test.want)
			}
		})
	}
}
