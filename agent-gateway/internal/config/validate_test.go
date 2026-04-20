package config

import (
	"strings"
	"testing"
)

func TestValidateNoInterceptHosts_AcceptsRealEntries(t *testing.T) {
	for _, p := range []string{
		"api.example.com",
		"*.googleapis.com",
		"*.internal",
		"*.k8s.local",
		"localhost",
		"a",
	} {
		t.Run(p, func(t *testing.T) {
			if _, err := validateNoInterceptHosts([]string{p}); err != nil {
				t.Errorf("expected %q to validate, got: %v", p, err)
			}
		})
	}
}

func TestValidateNoInterceptHosts_AcceptsEmptyList(t *testing.T) {
	if _, err := validateNoInterceptHosts(nil); err != nil {
		t.Errorf("nil list: %v", err)
	}
	if _, err := validateNoInterceptHosts([]string{}); err != nil {
		t.Errorf("empty list: %v", err)
	}
}

func TestValidateNoInterceptHosts_RejectsWildcardOnly(t *testing.T) {
	for _, p := range []string{
		"*",
		"**",
		"*.*",
		"**.**",
		"***",
		"*.*.*",
		".",
		"..",
	} {
		t.Run(p, func(t *testing.T) {
			_, err := validateNoInterceptHosts([]string{p})
			if err == nil {
				t.Fatalf("expected %q to be rejected", p)
			}
			if !strings.Contains(err.Error(), "matches every") {
				t.Errorf("error message should explain the rule, got: %v", err)
			}
		})
	}
}

func TestValidateNoInterceptHosts_RejectsEmptyEntry(t *testing.T) {
	for _, p := range []string{"", " ", "\t", "\n  "} {
		t.Run("blank", func(t *testing.T) {
			_, err := validateNoInterceptHosts([]string{p})
			if err == nil {
				t.Fatalf("expected %q to be rejected", p)
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Errorf("error should say empty, got: %v", err)
			}
		})
	}
}

func TestValidateNoInterceptHosts_PointsAtBadIndex(t *testing.T) {
	_, err := validateNoInterceptHosts([]string{"api.example.com", "**", "other.example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "[1]") {
		t.Errorf("error should name the offending index, got: %v", err)
	}
}
