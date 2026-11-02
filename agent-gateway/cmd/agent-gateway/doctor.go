package main

import (
	"bytes"
	"context"
	"database/sql"
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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
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
	Result    string                 `json:"result"`
	Checks    []doctorCheck          `json:"checks"`
	System    *contract.SystemStatus `json:"system,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var jsonOutput, verbose bool
	var address, bearerFile string
	command := &cobra.Command{Use: "doctor", Short: "Check setup and diagnose problems", Long: "Inspect the selected installation without changing it. Automatically read bounded readiness and authenticated status using its administrator bearer. Live evidence is attributed only when installation identities match. Never initializes, repairs, opens writable SQLite, refreshes credentials, starts upstreams or checks client CA trust. --verbose adds supporting facts without extra work. A completed checklist exits zero even when attention is needed; JSON result is healthy, attention or incomplete. Usage/selection/output errors remain command failures.", Example: "  agent-gateway doctor\n  agent-gateway doctor --verbose\n  agent-gateway doctor --json"}
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
		add := func(name, state, path, detail, action string) {
			result.Checks = append(result.Checks, doctorCheck{name, state, path, detail, action})
		}
		initCommand, _ := renderInstallationCommand("agent-gateway init", layout.Root)
		rootErr := gatewaypaths.InspectRoot(layout.Root)
		switch {
		case rootErr == nil:
			add("installation", "ok", layout.Root, "Selected root ownership, type and permissions verified.", "")
		case errors.Is(rootErr, os.ErrNotExist):
			add("installation", "absent", layout.Root, "No installation directory.", initCommand)
		default:
			add("installation", "failed", layout.Root, doctorCause("inspect root", layout.Root, rootErr), "Correct the reported path issue; do not delete installation files.")
		}
		expectedID := ""
		expectedRoot, _ := gatewaypaths.DiagnosticRootIdentity(layout.Root)
		if rootErr == nil {
			evidencePath := filepath.Join(layout.Root, gatewaypaths.InitializationName)
			if _, statErr := os.Lstat(evidencePath); !errors.Is(statErr, os.ErrNotExist) {
				plan, complete, readErr := gatewaypaths.ReadInitialization(layout)
				switch {
				case readErr != nil:
					add("initialization", "failed", evidencePath, doctorCause("read initialization evidence", evidencePath, readErr), "Preserve all artifacts; unknown initialization state cannot be resumed safely.")
				case complete:
					expectedID = plan.Installation
					add("initialization", "ok", evidencePath, "Recorded initialization completed; current usability is independent.", "")
				default:
					expectedID = plan.Installation
					resume, _ := renderInstallationCommand("agent-gateway init --resume", layout.Root)
					add("initialization", "incomplete", evidencePath, "Recognized interrupted initialization; serve will not adopt it.", resume)
				}
			}
			rotationPath := filepath.Join(layout.Root, gatewaypaths.KeyRotationName)
			if _, rotationErr := os.Lstat(rotationPath); !errors.Is(rotationErr, os.ErrNotExist) {
				action, _ := renderInstallationCommand("agent-gateway maintenance rotate-master-key --recover --dry-run", layout.Root)
				add("key rotation", "incomplete", rotationPath, "Retained key-rotation evidence requires dedicated stopped diagnosis; no key was read.", action)
			}
		}
		presence := func(name, path string) {
			if rootErr != nil && !errors.Is(rootErr, os.ErrNotExist) && filepath.Dir(path) == layout.Root {
				add(name, "not-checked", path, "Selected root is unsafe; its files were not inspected.", "")
				return
			}
			fileErr := gatewaypaths.ValidateOwnerOnlyFile(path)
			switch {
			case fileErr == nil:
				add(name, "present", path, "Owner-only file present; contents and usability not established.", "")
			case errors.Is(fileErr, os.ErrNotExist):
				action := initCommand
				if rootErr == nil {
					action = "Preserve existing installation files and inspect setup; init never repairs an established installation."
				}
				if name == "administrator credential" && rootErr == nil {
					action = "Select a known --admin-bearer-file; missing material does not authorize a credential reset."
				}
				add(name, "absent", path, "File is absent.", action)
			default:
				add(name, "failed", path, doctorCause("inspect file", path, fileErr), "Correct the reported path issue; do not delete the file.")
			}
		}
		presence("storage", layout.Database)
		running, ownershipErr := gatewaypaths.ProbeOwnership(layout.Root)
		if ownershipErr == nil {
			state := "stopped"
			if running {
				state = "running"
			}
			add("process ownership", state, layout.Lock, "Existing lock observation; not listener identity or clean-shutdown evidence.", "")
		} else {
			add("process ownership", "not-checked", layout.Lock, doctorCause("probe ownership", layout.Lock, ownershipErr), "")
		}
		selectedBearer := bearerFile
		if selectedBearer == "" {
			selectedBearer = layout.AdminBearer
		}
		selectedBearer, err = filepath.Abs(selectedBearer)
		if err != nil {
			return fail("The --admin-bearer-file path cannot be resolved.")
		}
		presence("administrator credential", selectedBearer)
		presence("public CA certificate", filepath.Join(layout.Root, gatewaypaths.PublicCertificateName))
		add("external client CA trust", "unverified", "", "Client trust was not inspected or changed; public certificate presence is not client adoption.", "")
		add("protected CA signing material", "not-checked", "", "No protected-keyring probe was performed; public certificate presence is not signing capability.", "")
		// Hold the existing stopped lock across immutable metadata queries. Never use
		// full maintenance inspection here: it hashes/scans storage and traffic.
		if rootErr == nil && !running {
			owner, openErr := gatewaypaths.AcquireStoppedControl(layout.Root)
			if openErr == nil {
				facts := []contract.CredentialSetupFact{}
				ctx, cancel := context.WithTimeout(command.Context(), time.Second)
				identity, readErr := storage.ReadDiagnosticMetadata(ctx, owner, func(tx *sql.Tx) error { var err error; facts, err = credentialSetupFacts(ctx, tx); return err })
				cancel()
				readErr = errors.Join(readErr, owner.Close())
				if readErr == nil {
					if expectedID != "" && expectedID != identity.InstallationID {
						add("storage metadata", "failed", layout.Database, "Database and initialization identities differ.", "Preserve artifacts and verify the selected installation; do not resume setup.")
					} else {
						expectedID = identity.InstallationID
						add("storage metadata", "ok", layout.Database, "Closed-generation metadata read only; integrity and credential usability were not checked.", "")
						appendDoctorSetup(&result, facts)
					}
				} else {
					state, action := "not-checked", ""
					if !errors.Is(readErr, storage.ErrInspectionUnavailable) && !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, context.DeadlineExceeded) {
						state, action = "failed", "Preserve all storage and marker files; inspect the reported cause before a controlled startup. No repair was attempted."
					}
					add("storage metadata", state, layout.Database, doctorCause("read closed metadata", layout.Database, readErr), action)
				}
			} else {
				add("storage metadata", "not-checked", layout.Database, doctorCause("acquire existing stopped ownership", layout.Root, openErr), "")
			}
		}
		ctx, cancel := context.WithTimeout(command.Context(), 3*time.Second)
		defer cancel()
		readiness, detail := doctorReadiness(ctx, address)
		action := ""
		if readiness != "ok" && rootErr == nil {
			action = "Check the running Gateway's --address and logs."
			if ownershipErr == nil && !running {
				serve, _ := renderInstallationCommand("agent-gateway serve", layout.Root)
				action = "Start Gateway when setup is complete: " + serve
			}
		}
		add("runtime readiness", readiness, "", detail, action)
		if rootErr != nil {
			add("authenticated live status", "not-checked", selectedBearer, "Selected installation is absent or unsafe; live status was not authenticated.", "")
		} else {
			status, statusErr := doctorOnlineStatus(ctx, address, selectedBearer)
			switch {
			case statusErr != nil:
				add("authenticated live status", "unavailable", "", doctorCause("read live status", "", statusErr), "Check --address and select a known --admin-bearer-file; never reset solely because a file is missing.")
			case !contract.ValidAuditID(status.InstallationID) || expectedID == "" && (expectedRoot == "" || status.InstallationRoot == ""):
				add("authenticated live status", "unverified", "", "Authentication succeeded but installation identity could not be established; listener status is not selected-installation evidence.", "Verify the selected data directory and Gateway version/address.")
			case expectedID != "" && status.InstallationID != expectedID || status.InstallationRoot != "" && status.InstallationRoot != expectedRoot:
				add("authenticated live status", "failed", "", "Authenticated listener belongs to a different installation; its status was not adopted.", "Select the matching --address, --data-dir and administrator credential.")
			default:
				result.System = &status
				add("authenticated live status", "ok", "", "Authenticated status matches the selected installation; independent failed checks remain unresolved.", "")
				appendDoctorSystem(&result, status)
				if status.Setup != nil && status.Setup.State == "ok" {
					appendDoctorSetup(&result, status.Setup.Facts)
				} else {
					add("credential setup", "not-checked", "", "Live setup metadata unavailable; no credential usability was inferred.", "")
				}
				servers, serversErr := doctorServers(ctx, address, selectedBearer)
				if serversErr != nil {
					add("upstream status", "not-checked", "", doctorCause("read server status", address, serversErr), "Inspect mcp server list when the control API is available.")
				} else {
					appendDoctorServers(&result, servers)
				}
			}
		}
		result.Result = doctorSummary(result.Checks)
		return offlineResult(command, jsonOutput, result, renderDoctorChecks(result, verbose))
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "structured independent facts and summary")
	command.Flags().BoolVar(&verbose, "verbose", false, "show supporting healthy and unverified observations")
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
		return "ok", detail + "; listener readiness alone is not selected-installation identity or upstream health."
	}
	if response.StatusCode == http.StatusServiceUnavailable && bytes.Equal(bytes.TrimSpace(response.Body), []byte(`{"status":"not_ready"}`)) {
		return "not-ready", detail + "; listener reports not ready."
	}
	return "failed", detail + "; expected status/body pair 200/ready or 503/not_ready; response body withheld."
}

func doctorGET(ctx context.Context, address, bearerPath, path string) (controlclient.Response, error) {
	bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{FilePath: bearerPath})
	if err != nil {
		return controlclient.Response{}, fmt.Errorf("authentication=not_attempted; acquire administrator bearer %s: %w", diagnostics.Text(bearerPath, 160), err)
	}
	client, err := controlclient.New(address, controlclient.TransportOptions{ConnectTimeout: time.Second, HeaderTimeout: time.Second, RequestTimeout: 2 * time.Second})
	if err != nil {
		return controlclient.Response{}, fmt.Errorf("authentication=not_attempted; validate live status address: %w", err)
	}
	header, err := controlclient.RequestMetadata(controlclient.RequestMetadataOptions{Bearer: bearer})
	if err != nil {
		return controlclient.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return controlclient.Response{}, fmt.Errorf("authentication=not_attempted; shared doctor context expired before live status: %w", err)
	}
	response, err := client.Do(ctx, controlclient.Request{Method: http.MethodGet, Path: path, Header: header})
	if err != nil {
		detail := diagnostics.Snapshot("doctor", "GET "+path, address, err, bearer)
		detail.Effect = "authentication=unknown"
		if controlclient.FailureHandoff(err) == controlclient.HandoffNone {
			detail.Effect = "authentication=not_attempted"
		}
		return response, diagnostics.WithDetail(err, detail)
	}
	if response.StatusCode != http.StatusOK {
		code := "unvalidated"
		if problem := controlclient.EvaluateResponse(response); problem != nil && problem.Status != nil {
			code = problem.Code
		}
		return response, fmt.Errorf("GET %s %s authentication=attempted http_status=%d problem_code=%s; expected HTTP 200; response body withheld", path, diagnostics.Text(address, 160), response.StatusCode, code)
	}
	return response, nil
}

func doctorOnlineStatus(ctx context.Context, address, bearerPath string) (contract.SystemStatus, error) {
	var result contract.SystemStatus
	response, err := doctorGET(ctx, address, bearerPath, "/api/v2/system-status")
	if err != nil {
		return result, err
	}
	if err := controlclient.DecodeResponse(response.Body, &result); err != nil {
		return result, fmt.Errorf("GET /api/v2/system-status %s authentication=attempted http_status=200; expected system-status representation; response body withheld: %w", diagnostics.Text(address, 160), err)
	}
	return result, nil
}

func doctorServers(ctx context.Context, address, bearerPath string) ([]serverWire, error) {
	items := []serverWire{}
	cursor := ""
	for pageIndex := 0; pageIndex < 2; pageIndex++ {
		path, err := controlclient.BuildListPath("/api/v2/mcp/servers", controlclient.ListOptions{Limit: 50, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		response, err := doctorGET(ctx, address, bearerPath, path)
		if err != nil {
			return nil, err
		}
		var page contract.Collection[serverWire]
		if err := controlclient.DecodeResponse(response.Body, &page); err != nil {
			return nil, err
		}
		if len(page.Items) > 50 {
			return nil, errors.New("server status exceeded page bound")
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil {
			return items, nil
		}
		if *page.NextCursor == "" || *page.NextCursor == cursor {
			break
		}
		cursor = *page.NextCursor
	}
	return nil, errors.New("bounded server status incomplete; inspect mcp server list")
}
