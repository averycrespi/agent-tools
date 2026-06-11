package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultToolCallTimeoutDoesNotReuseDiscoveryTimeout(t *testing.T) {
	require.Equal(t, 15*time.Second, brokerTimeout)
	require.Zero(t, callTimeout)
}
