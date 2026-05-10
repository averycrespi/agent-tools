package main

import (
	"fmt"
	"os"

	adconfig "github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	templateListCmd.RunE = listTemplates
}

func listTemplates(cmd *cobra.Command, _ []string) error {
	templates, err := adconfig.DiscoverTemplates(cfg.TemplateDirs)
	if err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(os.Stdout, templates)
	}
	for _, tmpl := range templates {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", tmpl.Name, tmpl.Description, tmpl.Path); err != nil {
			return err
		}
	}
	return nil
}
