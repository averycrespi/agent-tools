//go:build windows

package main

import osExec "os/exec"

func detachCommand(_ *osExec.Cmd) {}
