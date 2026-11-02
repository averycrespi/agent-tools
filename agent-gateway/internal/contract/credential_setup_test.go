package contract

import "testing"

func TestCredentialMaterialStateDistinguishesUnsetAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		revision  uint64
		available bool
		want      CredentialMaterialState
	}{
		{0, false, CredentialNotConfigured}, {1, false, CredentialMaterialUnavailable}, {2, true, CredentialMaterialReady},
	} {
		got := MaterialState(tc.revision, tc.available)
		if got != tc.want || !got.Valid(tc.available) {
			t.Fatalf("unexpected material state %q", got)
		}
	}
	if CredentialNotConfigured.Valid(true) || CredentialMaterialReady.Valid(false) || CredentialMaterialState("unknown").Valid(false) {
		t.Fatal("invalid material state accepted")
	}
}
