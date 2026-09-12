package invocation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHistorySearchBrowserSemantics(t *testing.T) {
	for _, test := range []struct {
		candidate, query, locale string
		match                    bool
	}{
		{"Café Investigator", "cafe investgiator", "en-US", true},
		{"historical_library.lookup", "historical lokoup", "en-US", true},
		{"lookup", "lookpu", "en-US", true},
		{"lookup", "lokp", "en-US", false},
		{"abc", "acb", "en-US", false},
		{"build123", "build132", "en-US", false},
		{"build123", "123", "en-US", true},
		{"ΙΣ", "ις", "el", true},
		{"Istanbul", "ıstanbul", "tr", true},
		{"Istanbul", "istanbul", "tr", true},
		{"I", "i", "tr", false},
		{"unavailable", "\u0301", "en-US", true},
		{"", "not found", "en-US", false},
		{"Ｆｕｌｌｗｉｄｔｈ", "fullwidth", "en-US", true},
	} {
		t.Run(test.candidate+"/"+test.query+"/"+test.locale, func(t *testing.T) {
			assert.Equal(t, test.match, newHistorySearch(test.query, test.locale).matches(test.candidate))
		})
	}
}
