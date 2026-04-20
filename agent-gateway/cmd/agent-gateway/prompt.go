package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// confirm prompts for a y/N confirmation and returns true on affirmative.
//
// When force is true, the prompt is skipped and the function returns
// (true, nil) immediately. When isTTY is false and force is false, the
// function refuses without reading from in — scripts must opt in to
// destructive actions explicitly via --force.
func confirm(in io.Reader, out io.Writer, isTTY, force bool, message string) (bool, error) {
	if force {
		return true, nil
	}
	if !isTTY {
		return false, errors.New("refusing destructive action: stdin is not a TTY; pass --force to bypass confirmation")
	}
	_, _ = fmt.Fprintf(out, "%s [y/N]: ", message)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		_, _ = fmt.Fprintln(out, "cancelled")
		return false, nil
	}
	return true, nil
}

// confirmViaTTY prompts via /dev/tty for commands whose stdin is already
// consumed (e.g. secret update, which reads the new value from stdin).
// When force is true, returns (true, nil) without opening /dev/tty.
func confirmViaTTY(out io.Writer, force bool, message string) (bool, error) {
	if force {
		return true, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, errors.New("refusing destructive action: /dev/tty unavailable; pass --force to bypass confirmation")
	}
	defer func() { _ = tty.Close() }()
	return confirm(tty, out, true, false, message)
}
