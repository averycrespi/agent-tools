package main

import "testing"

func TestRootCommandSilencesCobraErrors(t *testing.T) {
	if !rootCmd.SilenceErrors {
		t.Fatalf("expected root command to silence Cobra errors because main logs Execute errors")
	}
}
