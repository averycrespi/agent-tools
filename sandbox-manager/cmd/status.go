package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/spf13/cobra"
)

type statusService interface {
	Status() (lima.Status, error)
}

type statusJSON struct {
	Status string `json:"status"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sandbox status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		return runStatusCommand(svc, jsonOutput, cmd.OutOrStdout())
	},
}

func runStatusCommand(service statusService, jsonOutput bool, out io.Writer) error {
	status, err := service.Status()
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(statusJSON{Status: machineStatus(status)})
	}
	_, err = fmt.Fprintln(out, humanStatus(status))
	return err
}

func humanStatus(status lima.Status) string {
	switch status {
	case lima.StatusRunning:
		return "running"
	case lima.StatusStopped:
		return "stopped"
	case lima.StatusNotCreated:
		return "not created"
	default:
		return string(status)
	}
}

func machineStatus(status lima.Status) string {
	switch status {
	case lima.StatusRunning:
		return "running"
	case lima.StatusStopped:
		return "stopped"
	case lima.StatusNotCreated:
		return "not_created"
	default:
		return string(status)
	}
}

func init() {
	statusCmd.Flags().Bool("json", false, "output status as JSON")
	rootCmd.AddCommand(statusCmd)
}
