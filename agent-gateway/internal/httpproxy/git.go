package httpproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

func (e *Engine) git(w http.ResponseWriter, r *http.Request, lease *authorization.Lease, target httppolicy.Request, address *remote.ProxyAddress, repository contract.GitRepository, profile contract.GitRoutingProfile) {
	started := time.Now()
	e.options.Observations.Request(diagnostics.Git)
	defer func() { e.options.Observations.Latency(diagnostics.Git, diagnostics.RequestStage, time.Since(started)) }()
	controller := http.NewResponseController(w)
	if r.ProtoMajor == 1 {
		if err := controller.EnableFullDuplex(); err != nil {
			reject(w, http.StatusBadGateway)
			return
		}
	}
	// A single absolute deadline covers all command controls, the PACK signature,
	// and exact end-of-body probes; individual reads never renew it.
	if err := controller.SetReadDeadline(time.Now().Add(gitwire.ParseTimeout)); err != nil {
		reject(w, http.StatusBadRequest)
		return
	}
	request, err := gitwire.New(r.Context(), target, repository, profile.Revision, r.Header, r.Body)
	if err != nil {
		e.rejectGit(w, r, lease, "unsupported")
		return
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		reject(w, http.StatusBadRequest)
		return
	}
	identity, _ := e.options.Evidence.PrepareIdentity()
	result, err := e.options.Admissions.AdmitGit(r.Context(), lease, identity, request, address.Facts(), e.options.GitMaterials)
	if err != nil || !result.DispatchAuthorized {
		if err != nil {
			e.observeRejection(started, result.FailureStage, result.FailureCause, diagnostics.WithDetail(err, result.FailureDetail), w)
			if errors.Is(err, invocation.ErrTrafficCapacity) {
				rejectCapacity(w, http.StatusServiceUnavailable)
			} else {
				reject(w, http.StatusServiceUnavailable)
			}
			return
		}
		if result.Execution.CredentialUnavailable {
			reject(w, http.StatusServiceUnavailable)
			return
		}
		reject(w, http.StatusForbidden)
		return
	}
	e.options.Observations.Latency(diagnostics.Git, diagnostics.AdmissionStage, time.Since(started))
	e.options.Observations.Execution(diagnostics.Git)
	executionStarted := time.Now()
	completion := contract.GitTrafficCompletion{Outcome: "prestart_failure"}
	defer func() {
		e.observeCompletion(diagnostics.Git, completion.Outcome, executionStarted)
		e.options.Observations.GitReport(completion.ReportedResult)
		e.completeGit(result, identity, completion)
	}()
	header := r.Header.Clone()
	if result.Material != nil {
		header, err = result.Material.Apply(repository.URL, header)
		if err != nil {
			completion.Failure = "credential_unavailable"
			reject(w, http.StatusServiceUnavailable)
			return
		}
	}
	stripHopHeaders(header)
	if remote.ValidateProxyHeaders(header) != nil {
		reject(w, http.StatusBadRequest)
		return
	}
	body, err := request.Dispatch()
	if err != nil {
		reject(w, http.StatusForbidden)
		return
	}
	upload := &countedBody{ReadCloser: body, controller: controller, done: make(chan struct{})}
	defer func() {
		_ = upload.Close()
		completion.BytesSent = upload.count.Load()
		completion.TransferComplete = completion.TransferComplete && (upload.eof.Load() || r.ContentLength >= 0 && completion.BytesSent == r.ContentLength)
		if !completion.TransferComplete {
			completion.ReportedResult = ""
			completion.RefOutcomes = nil
		}
		if request.Operation() != "push" && completion.TransferComplete {
			completion.Outcome = "nonmutation"
		}
	}()
	completion.Outcome = "outcome_unknown"
	if request.Operation() == "push" {
		header.Set("Accept-Encoding", "identity")
	}
	response, err := address.ProxyExchange(r.Context(), request.Target(), header, upload, r.ContentLength, result.Execution.PrivateNetwork, e.roots)
	if err != nil {
		e.observeFailure(w, diagnostics.ProxyExchange, err)
		reject(w, upstreamStatus(err))
		return
	}
	defer func() { _ = response.Body.Close() }()
	stripHopHeaders(response.Header)
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	completion.Status = response.StatusCode
	writer := &streamWriter{writer: w, controller: controller}
	if err := writer.Flush(); err != nil {
		e.observeTransfer(w, "downstream_flush", err)
		panic(http.ErrAbortHandler)
	}
	var reader io.Reader = response.Body
	var observer *gitwire.StatusObserver
	encodings := response.Header.Values("Content-Encoding")
	if request.Operation() == "push" && response.StatusCode == http.StatusOK && (len(encodings) == 0 || len(encodings) == 1 && encodings[0] == "identity") && len(response.Header.Values("Content-Type")) == 1 && response.Header.Get("Content-Type") == "application/x-git-receive-pack-result" && !response.Uncompressed {
		observer = request.ObserveStatus()
		reader = io.TeeReader(reader, observer)
	}
	n, err := io.CopyBuffer(writer, reader, make([]byte, contract.HTTPProxyBufferBytes))
	completion.BytesReceived = n
	if err != nil {
		e.observeTransfer(w, "upstream_read", err)
		panic(http.ErrAbortHandler)
	}
	completion.TransferComplete = true
	if observer != nil {
		if report := observer.Result(); report != "unknown" {
			completion.ReportedResult = report
			if result.RefEvidence != nil {
				refs := make([]string, 0, len(result.RefEvidence.Refs))
				for _, ref := range result.RefEvidence.Refs {
					refs = append(refs, ref.Name)
				}
				completion.RefOutcomes = observer.RefOutcomes(refs)
			}
		}
	}
}
func (e *Engine) rejectGit(w http.ResponseWriter, r *http.Request, lease *authorization.Lease, reason string, statuses ...int) {
	identity, _ := e.options.Evidence.PrepareIdentity()
	if e.options.Admissions.RejectGit(r.Context(), lease, identity, reason) != nil {
		reject(w, http.StatusServiceUnavailable)
		return
	}
	status := http.StatusBadRequest
	if reason == "destination_unavailable" {
		status = http.StatusBadGateway
	}
	if reason == "repository_unavailable" {
		status = http.StatusForbidden
	}
	if len(statuses) != 0 {
		status = statuses[0]
	}
	reject(w, status)
}

func (e *Engine) completeGit(result invocation.GitAdmissionResult, identity invocation.PreparedAdmission, completion contract.GitTrafficCompletion) {
	result.Settle()
	now := e.options.Now().UTC()
	start, err := time.Parse(time.RFC3339Nano, identity.AdmittedAt)
	if err != nil {
		return
	}
	completion.CompletedAt = now.Format(contract.AuditTimestampLayout)
	completion.DurationMS = max(0, now.Sub(start).Milliseconds())
	ctx, cancel := context.WithTimeout(context.Background(), contract.HTTPProxyDrainTimeout)
	defer cancel()
	_ = e.options.Admissions.CompleteGit(ctx, result, completion)
}
