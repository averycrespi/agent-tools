package contract

import "strings"

// NormalizeHostname accepts ASCII DNS labels, not IP literals or absolute DNS names.
func NormalizeHostname(host string) (string, bool) {
	if len(host) == 0 || len(host) > 253 {
		return "", false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, value := range label {
			if (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') && (value < '0' || value > '9') && value != '-' {
				return "", false
			}
		}
	}
	// A numeric final label would also admit noncanonical IPv4 spellings.
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", false
	}
	return strings.ToLower(host), true
}
