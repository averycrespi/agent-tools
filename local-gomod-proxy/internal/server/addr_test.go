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
		{"default", "127.0.0.1:7070", false},
		{"loopback alt", "127.0.0.2:7070", false},
		{"ipv6 loopback", "[::1]:7070", false},
		{"localhost name", "localhost:7070", false},
		{"all interfaces v4", "0.0.0.0:7070", true},
		{"all interfaces v6", "[::]:7070", true},
		{"empty host", ":7070", true},
		{"lan ip", "192.168.1.10:7070", true},
		{"public ip", "8.8.8.8:7070", true},
		{"unresolved name", "example.com:7070", true},
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
