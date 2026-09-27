package main

import (
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
)

func selectedHTTPProxy(command *cobra.Command, legacyManaged bool) (string, error) {
	if command.Flags().Lookup("http-proxy-listen") == nil {
		return "", nil
	}
	authority, _ := command.Flags().GetString("http-proxy-listen")
	explicit := command.Flags().Changed("http-proxy-listen")
	clearProxy, _ := command.Flags().GetBool("clear-http-proxy-listen")
	if clearProxy && explicit {
		return "", controlclient.NewInputError("Choose --http-proxy-listen or --clear-http-proxy-listen, not both.")
	}
	if explicit && authority == "" {
		return "", controlclient.NewInputError("Provide a proxy authority or use --clear-http-proxy-listen.")
	}
	if clearProxy || !explicit && legacyManaged {
		return "", nil
	}
	return authority, nil
}
