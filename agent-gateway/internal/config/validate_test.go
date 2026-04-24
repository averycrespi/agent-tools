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

func TestValidateNoInterceptHosts_WarnsPublicSuffix(t *testing.T) {
	for _, p := range []string{
		"*.com",
		"**.com",
		"*.co.uk",
		"com",
		"*.*.com",
	} {
		t.Run(p, func(t *testing.T) {
			warnings, err := validateNoInterceptHosts([]string{p})
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", p, err)
			}
			var found bool
			for _, w := range warnings {
				if strings.Contains(w, "public suffix") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected public-suffix warning for %q, got: %v", p, warnings)
			}
		})
	}
}

func TestValidateNoInterceptHosts_NoPublicSuffixWarningForSafeEntries(t *testing.T) {
	for _, p := range []string{
		"*.example.com",
		"api.example.com",
		"*.googleapis.com",
		"*.internal",
		"*.k8s.local",
		"localhost",
	} {
		t.Run(p, func(t *testing.T) {
			warnings, err := validateNoInterceptHosts([]string{p})
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", p, err)
			}
			for _, w := range warnings {
				if strings.Contains(w, "public suffix") {
					t.Errorf("unexpected public-suffix warning for %q: %q", p, w)
				}
			}
		})
	}
}

// TestValidateConfig_ListenAddrWiring locks in that validateConfig actually
// calls validateListenAddrs. If a future refactor accidentally drops that
// call, these cases fail — a non-loopback listen must be rejected by the
// outer entry point, not just by the inner helper.
func TestValidateConfig_ListenAddrWiring(t *testing.T) {
	newCfg := func(proxy, dashboard string) *Config {
		return &Config{
			Proxy:     ProxyConfig{Listen: proxy},
			Dashboard: DashboardConfig{Listen: dashboard},
		}
	}

	t.Run("proxy listen non-loopback", func(t *testing.T) {
		_, err := validateConfig(newCfg("0.0.0.0:8220", "127.0.0.1:8221"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "proxy.listen:") {
			t.Errorf("error should name proxy.listen, got: %v", err)
		}
	})

	t.Run("both listeners non-loopback aggregate", func(t *testing.T) {
		_, err := validateConfig(newCfg("0.0.0.0:8220", "0.0.0.0:8221"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "proxy.listen:") {
			t.Errorf("error should name proxy.listen, got: %v", err)
		}
		if !strings.Contains(msg, "dashboard.listen:") {
			t.Errorf("error should name dashboard.listen (errors.Join aggregation), got: %v", err)
		}
	})

	t.Run("both loopback passes", func(t *testing.T) {
		if _, err := validateConfig(newCfg("127.0.0.1:8220", "[::1]:8221")); err != nil {
			t.Errorf("expected valid loopback config to pass, got: %v", err)
		}
	})
}
