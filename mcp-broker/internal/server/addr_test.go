package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateLoopbackAddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"default", "127.0.0.1:8200", false},
		{"loopback alt", "127.0.0.2:8200", false},
		{"ipv6 loopback", "[::1]:8200", false},
		{"localhost name", "localhost:8200", false},
		{"all interfaces v4", "0.0.0.0:8200", true},
		{"all interfaces v6", "[::]:8200", true},
		{"empty host", ":8200", true},
		{"lan ip", "192.168.1.10:8200", true},
		{"public ip", "8.8.8.8:8200", true},
		{"unresolved name", "example.com:8200", true},
		{"no port", "127.0.0.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoopbackAddr(tc.addr)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
