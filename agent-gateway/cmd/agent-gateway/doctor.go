package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/spf13/cobra"
)

type doctorCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}

type doctorResult struct {
	Operation string                 `json:"operation"`
	DataDir   string                 `json:"data_dir"`
	Selection string                 `json:"selection"`
	Checks    []doctorCheck          `json:"checks"`
	System    *contract.SystemStatus `json:"system,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var jsonOutput, verifyStorage, online, verbose bool
	var address, bearerFile string
	command := &cobra.Command{Use: "doctor", Short: "Check setup and diagnose problems", Long: "Never initializes, recovers, rotates authority or changes services. Presence is not usability. --verify-storage performs expensive stopped, closed-generation inspection; --online reads authenticated live status. Protected keyring access is not performed.", Example: "  agent-gateway doctor\n  agent-gateway doctor --verify-storage --json"}
	fail := func(message string) error {
		return writeOfflineProblem(command, offlineMode(jsonOutput), offlineUsageProblem(message, "agent-gateway doctor"))
	}
	command.Args = func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fail("Doctor accepts no positional arguments.")
		}
		return nil
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		root, _ := command.Root().PersistentFlags().GetString("data-dir")
		layout, err := gatewaypaths.Resolve(root)
		if err != nil {
			return writeOfflineProblem(command, offlineMode(jsonOutput), installationSelectionProblem(err))
		}
		result := doctorResult{Operation: "doctor", DataDir: layout.Root, Selection: "account-home default", Checks: []doctorCheck{}}
		if root != "" {
			result.Selection = "--data-dir"
		} else if os.Getenv("XDG_DATA_HOME") != "" {
			result.Selection = "XDG_DATA_HOME default"
		}
		next, _ := renderInstallationCommand("agent-gateway init", layout.Root)
		add := func(name, state, path, detail, action string) {
			result.Checks = append(result.Checks, doctorCheck{name, state, path, detail, action})
		}
		rootErr := gatewaypaths.InspectRoot(layout.Root)
		if err := rootErr; err == nil {
			add("installation", "ok", layout.Root, "Selected root ownership, type and permissions verified.", "")
		} else if errors.Is(err, os.ErrNotExist) {
			add("installation", "absent", layout.Root, "No installation directory.", next)
		} else {
			detail := doctorCause("inspect root", layout.Root, err)
			add("installation", "failed", layout.Root, detail, "Correct the reported path issue; do not delete installation files.")
		}
		presence := func(name, path string) {
			if rootErr != nil && !errors.Is(rootErr, os.ErrNotExist) && filepath.Dir(path) == layout.Root {
				add(name, "not-checked", path, "Selected root is unsafe; its files were not inspected.", "Correct the selected directory before inspecting its files.")
				return
			}
			err := gatewaypaths.ValidateOwnerOnlyFile(path)
			switch {
			case err == nil:
				add(name, "present", path, "Owner-only file present; contents and usability not established.", "")
			case errors.Is(err, os.ErrNotExist):
				action := next
				if name == "administrator credential" && rootErr == nil {
					action = "Select a known --admin-bearer-file; missing material does not authorize a credential reset."
				}
				add(name, "absent", path, "File is absent.", action)
			default:
				detail := doctorCause("inspect file", path, err)
				add(name, "failed", path, detail, "Correct the reported path issue; do not delete the file.")
			}
		}
		presence("storage", layout.Database)
		running, ownershipErr := gatewaypaths.ProbeOwnership(layout.Root)
		if ownershipErr == nil {
			state := "stopped"
			if running {
				state = "running"
			}
			add("process ownership", state, layout.Lock, "Existing lock observation; not readiness or clean-shutdown evidence.", "")
		} else {
			add("process ownership", "not-checked", layout.Lock, doctorCause("probe ownership", layout.Lock, ownershipErr), "Inspect the selected lock path; do not remove it on this evidence.")
		}
		defaultBearer := bearerFile == ""
		if defaultBearer {
			bearerFile = layout.AdminBearer
		}
		bearerFile, err = filepath.Abs(bearerFile)
		if err != nil {
			return fail("The --admin-bearer-file path cannot be resolved.")
		}
		presence("administrator credential", bearerFile)
		presence("public CA certificate", filepath.Join(layout.Root, gatewaypaths.PublicCertificateName))
		if last := &result.Checks[len(result.Checks)-1]; last.State == "absent" && running {
			last.Next = "Stop the selected Gateway, then run: " + next
		}
		add("protected CA signing material", "not-checked", "", "No protected-keyring probe was performed. Public certificate presence does not establish signing capability.", "")
		if verifyStorage {
			owner, openErr := gatewaypaths.AcquireStoppedExisting(layout.Root)
			switch {
			case openErr == nil:
				inspection, inspectErr := inspectInit(command.Context(), owner)
				closeErr := owner.Close()
				if inspectErr == nil && closeErr == nil {
					add("storage inspection", "ok", layout.Database, "Closed-generation identity and integrity verified: "+inspection.Snapshot.Identity.InstallationID, "Full mutation-time validation remains mandatory.")
					state := "absent"
					if inspection.CA.Present {
						state = "present"
					}
					add("CA metadata", state, "", "Public metadata inspected without keyring access; signing was not checked.", "")
				} else {
					detail := doctorCause("inspect closed storage and close ownership", layout.Database, errors.Join(inspectErr, closeErr))
					add("storage inspection", "failed", layout.Database, detail, "Retain WAL, journal and marker files; obtain a qualified stopped recovery plan.")
				}
			case errors.Is(openErr, gatewaypaths.ErrInUse):
				add("storage inspection", "not-checked", layout.Database, "Installation is running; offline inspection refused.", "Use --online for authenticated live status.")
			default:
				detail := doctorCause("acquire stopped ownership", layout.Root, openErr)
				add("storage inspection", "failed", layout.Database, detail, "Check the reported path without deleting installation files.")
			}
		}
		ctx, cancel := context.WithTimeout(command.Context(), 3*time.Second)
		defer cancel()
		readiness, detail := doctorReadiness(ctx, address)
		readinessNext := ""
		if readiness != "ok" && rootErr == nil {
			readinessNext = "Check the running Gateway's address and logs."
			if ownershipErr == nil && !running {
				serve, _ := renderInstallationCommand("agent-gateway serve", layout.Root)
				readinessNext = "Start Gateway when setup is complete: " + serve
			}
		}
		add("runtime readiness", readiness, "", detail, readinessNext)
		if online && defaultBearer && rootErr != nil {
			add("authenticated live status", "not-checked", bearerFile, "Selected installation is absent or unsafe; its default bearer was not read.", "Select an existing safe installation or a known --admin-bearer-file.")
		} else if online {
			status, statusErr := doctorOnlineStatus(ctx, address, bearerFile)
			if statusErr == nil {
				result.System = &status
				add("authenticated live status", "ok", "", "Public control API status received; independent failed checks remain unresolved.", "")
			} else {
				add("authenticated live status", "failed", "", doctorCause("read live status", "", statusErr), "Select a known --admin-bearer-file and the correct --address; never reset solely because a file is missing.")
			}
		}
		var human strings.Builder
		human.WriteString(renderDoctorChecks(result, verbose))
		if result.System != nil {
			body, marshalErr := json.Marshal(result.System)
			if marshalErr != nil {
				return marshalErr
			}
			table, tableErr := statusTable(body)
			if tableErr != nil {
				return tableErr
			}
			if err := controlclient.WriteSuccess(&human, controlclient.OutputHuman, body, table); err != nil {
				return err
			}
		}
		return offlineResult(command, jsonOutput, result, human.String())
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured checklist and errors")
	command.Flags().BoolVar(&verbose, "verbose", false, "show details for every check")
	command.Flags().BoolVar(&verifyStorage, "verify-storage", false, "perform expensive read-only stopped storage inspection")
	command.Flags().BoolVar(&online, "online", false, "read authenticated live status using the selected administrator bearer")
	command.Flags().StringVar(&address, "address", controlclient.DefaultAddress, "public control API address")
	command.Flags().StringVar(&bearerFile, "admin-bearer-file", "", "known owner-only administrator bearer file")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return fail(offlineFlagMessage(err)) })
	return command
}

func doctorCause(operation, resource string, err error) string {
	detail := diagnostics.Snapshot("doctor", operation, resource, err)
	return strings.TrimSpace(detail.Operation + " " + detail.Resource + ": " + detail.Explanation + " " + detail.Effect)
}

func doctorReadiness(ctx context.Context, address string) (string, string) {
	client, err := controlclient.New(address, controlclient.TransportOptions{ConnectTimeout: time.Second, HeaderTimeout: time.Second, RequestTimeout: 2 * time.Second})
	if err != nil {
		return "failed", doctorCause("validate readiness address", "", err)
	}
	response, err := client.Do(ctx, controlclient.Request{Method: http.MethodGet, Path: "/readyz"})
	if err != nil {
		return "unavailable", doctorCause("GET /readyz", address, err)
	}
	detail := fmt.Sprintf("GET /readyz %s http_status=%d; authentication=not_attempted", diagnostics.Text(address, 160), response.StatusCode)
	if response.StatusCode == http.StatusOK && bytes.Equal(bytes.TrimSpace(response.Body), []byte(`{"status":"ready"}`)) {
		return "ok", detail + "; listener readiness is not process identity, clean storage or upstream credential health."
	}
	if response.StatusCode == http.StatusServiceUnavailable && bytes.Equal(bytes.TrimSpace(response.Body), []byte(`{"status":"not_ready"}`)) {
		return "not-ready", detail + "; listener reports not ready."
	}
	return "failed", detail + "; expected status/body pair 200/ready or 503/not_ready; response body withheld."
}

func doctorOnlineStatus(ctx context.Context, address, bearerPath string) (contract.SystemStatus, error) {
	var result contract.SystemStatus
	bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{FilePath: bearerPath})
	if err != nil {
		return result, fmt.Errorf("authentication=not_attempted; acquire administrator bearer %s: %w", diagnostics.Text(bearerPath, 160), err)
	}
	client, err := controlclient.New(address, controlclient.TransportOptions{RequestTimeout: 2 * time.Second})
	if err != nil {
		return result, fmt.Errorf("authentication=not_attempted; validate live status address: %w", err)
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer})
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("authentication=not_attempted; shared doctor context expired before live status: %w", err)
	}
	response, err := client.Do(ctx, controlclient.Request{Method: http.MethodGet, Path: "/api/v2/system-status", Header: header})
	if err != nil {
		detail := diagnostics.Snapshot("doctor", "GET /api/v2/system-status", address, err, bearer)
		detail.Effect = "authentication=unknown"
		if controlclient.FailureHandoff(err) == controlclient.HandoffNone {
			detail.Effect = "authentication=not_attempted"
		}
		return result, diagnostics.WithDetail(err, detail)
	}
	if response.StatusCode != http.StatusOK {
		code := "unvalidated"
		if problem := controlclient.EvaluateResponse(response); problem != nil && problem.Status != nil {
			code = problem.Code
		}
		return result, fmt.Errorf("GET /api/v2/system-status %s authentication=attempted http_status=%d problem_code=%s; expected HTTP 200; response body withheld", diagnostics.Text(address, 160), response.StatusCode, code)
	}
	if err = controlclient.DecodeResponse(response.Body, &result); err != nil {
		return result, fmt.Errorf("GET /api/v2/system-status %s authentication=attempted http_status=200; expected system-status representation; response body withheld: %w", diagnostics.Text(address, 160), err)
	}
	return result, nil
}
