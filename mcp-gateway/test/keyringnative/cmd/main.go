package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: keyring-native-result <emit|validate>", 2)
	}
	switch os.Args[1] {
	case "emit":
		if len(os.Args) != 7 {
			fatal("emit requires result, platform, reason, deterministic status, and native status", 2)
		}
		write(keyringnative.NewResult(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]))
	case "validate":
		contents, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil {
			fatal("read native result", 2)
		}
		result, err := keyringnative.Parse(contents)
		if err != nil {
			fatal("invalid native result", 2)
		}
		write(result)
		if result.Result == keyringnative.ResultFailed {
			os.Exit(1)
		}
	default:
		fatal("unknown native result command", 2)
	}
}

func write(result keyringnative.Result) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fatal("write native result", 2)
	}
}

func fatal(message string, code int) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(code)
}
