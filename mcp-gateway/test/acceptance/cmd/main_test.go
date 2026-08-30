package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAcceptanceCommandRejectsRemovedModes(t *testing.T) {
	for _, argument := range []string{"--profile", "--profile=retired", "--task", "--milestone", "--qualify-external"} {
		assert.Equal(t, 2, run([]string{argument}), argument)
	}
}

func TestAcceptanceCommandRequiresOneClosedInterface(t *testing.T) {
	assert.Equal(t, 2, run(nil))
	assert.Equal(t, 2, run([]string{"unknown"}))
	assert.Equal(t, 2, run([]string{"accept"}))
	assert.Equal(t, 2, run([]string{"adopt-acceptance-report"}))
	assert.Equal(t, 2, run([]string{"qualify-external-evidence", "extra"}))
}
