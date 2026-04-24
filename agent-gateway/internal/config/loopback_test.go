package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
)

func TestValidateLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:8220", false},
		{"127.0.0.2:8220", false},
		{"[::1]:8220", false},
		{"localhost:8220", false},
		{"0.0.0.0:8220", true},
		{"8.8.8.8:8220", true},
		{":8220", true},
		{"127.0.0.1", true},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			err := config.ValidateLoopbackAddr(c.addr)
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
