package main

import "os"

func main() {
	if rootCmd.Execute() != nil {
		os.Exit(1)
	}
}
