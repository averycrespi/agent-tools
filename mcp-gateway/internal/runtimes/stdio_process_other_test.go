//go:build !darwin && !linux

package runtimes

func fixtureProcessGroup() int { return 0 }
