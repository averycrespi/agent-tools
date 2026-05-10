package main

import (
	"fmt"
	"strings"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	templateListCmd.RunE = listTemplates
	templateValidateCmd.RunE = validateTemplates
	templateShowCmd.RunE = showTemplate
	templateRenderCmd.RunE = renderTemplate
}

func listTemplates(cmd *cobra.Command, _ []string) error {
	templates, err := pdconfig.DiscoverTemplates(cfg.TemplateDirs)
	if err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), templates)
	}
	for _, tmpl := range templates {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", tmpl.Name, tmpl.Description, tmpl.Path); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplates(cmd *cobra.Command, args []string) error {
	var templates []pdconfig.Template
	if len(args) == 1 {
		tmpl, err := pdconfig.FindTemplate(cfg.TemplateDirs, args[0])
		if err != nil {
			return err
		}
		templates = []pdconfig.Template{tmpl}
	} else {
		var err error
		templates, err = pdconfig.DiscoverTemplates(cfg.TemplateDirs)
		if err != nil {
			return err
		}
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), templates)
	}
	for _, tmpl := range templates {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\tok\n", tmpl.Name); err != nil {
			return err
		}
	}
	return nil
}

func showTemplate(cmd *cobra.Command, args []string) error {
	tmpl, err := pdconfig.FindTemplate(cfg.TemplateDirs, args[0])
	if err != nil {
		return err
	}
	return output.JSON(cmd.OutOrStdout(), tmpl)
}

func renderTemplate(cmd *cobra.Command, args []string) error {
	tmpl, err := pdconfig.FindTemplate(cfg.TemplateDirs, args[0])
	if err != nil {
		return err
	}
	argv := pdconfig.RenderPiArgv(tmpl.Agent)
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), argv)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), shellJoin(argv))
	return err
}

func shellJoin(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool { return !isShellSafeChar(r) }) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func isShellSafeChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		strings.ContainsRune("_+-./:=,@%", r)
}
