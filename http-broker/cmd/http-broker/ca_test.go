package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

func TestCARotationRequiresConfirmationAndManualTrustRefresh(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	previous := caRotateConfirm
	t.Cleanup(func() {
		caRotateConfirm = previous
		caRotateCmd.SetOut(nil)
		caRotateCmd.SetErr(nil)
	})
	caRotateConfirm = false
	if err := caRotateCmd.RunE(caRotateCmd, nil); err == nil || !strings.Contains(err.Error(), "manually installed") {
		t.Fatalf("missing manual trust refresh warning: %v", err)
	}
	if _, err := os.Stat(paths.CACert()); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed rotation changed CA state: %v", err)
	}

	var output, diagnostics bytes.Buffer
	caRotateCmd.SetOut(&output)
	caRotateCmd.SetErr(&diagnostics)
	caRotateConfirm = true
	if err := caRotateCmd.RunE(caRotateCmd, nil); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(paths.CACert())
	if err != nil {
		t.Fatal(err)
	}
	if err := caRotateCmd.RunE(caRotateCmd, nil); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.CACert())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("confirmed rotation did not replace the certificate")
	}
	for _, phrase := range []string{"securely transfer and install", "every client trust store", "SIGHUP"} {
		if !strings.Contains(diagnostics.String(), phrase) {
			t.Errorf("missing guidance %q", phrase)
		}
	}
	if strings.Contains(output.String()+diagnostics.String(), "PRIVATE KEY") {
		t.Fatal("rotation output exposed key material")
	}
}
