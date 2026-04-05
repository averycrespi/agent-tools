package main

import (
	"os"
)

func main() {
	// Parse persistent flags early for --no-cache and --timeout during discovery.
	_ = rootCmd.ParseFlags(os.Args[1:])

	// Discovery must happen before Execute so that tool subcommand flags
	// are registered before cobra parses the command line.
	if err := buildTree(); err != nil {
		writeError(err)
		os.Exit(1)
	}

	if err := rootCmd.Execute(); err != nil {
		writeError(err)
		os.Exit(1)
	}
}
