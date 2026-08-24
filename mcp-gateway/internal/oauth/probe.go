package oauth

import (
	"context"
	"errors"
)

var ErrProbeRejected = errors.New("OAuth activation probe was rejected")

type ProbeOutcome string

const (
	ProbeSucceeded         ProbeOutcome = "succeeded"
	ProbeInvalidToken      ProbeOutcome = "invalid_token"
	ProbeInsufficientScope ProbeOutcome = "insufficient_scope"
)

type ProbeResult struct {
	Outcome           ProbeOutcome
	ChallengeMetadata []string
	ChallengeScopes   []string
}

type ActivationProbe interface {
	Probe(context.Context) (ProbeResult, error)
}

type RefreshInvoker interface {
	Refresh(context.Context, RefreshRequest) (RefreshResult, error)
}

type StepUpSink interface {
	StageStepUp(string, []string, []string, []string) error
}

func ProbeWithRefresh(ctx context.Context, serverID string, priorScopes []string, probe ActivationProbe, refresh RefreshInvoker, stepUp StepUpSink) error {
	if serverID == "" || probe == nil || refresh == nil || stepUp == nil {
		return ErrProbeRejected
	}
	result, err := probe.Probe(ctx)
	if err != nil {
		return err
	}
	switch result.Outcome {
	case ProbeSucceeded:
		return nil
	case ProbeInsufficientScope:
		if err := stepUp.StageStepUp(serverID, result.ChallengeMetadata, priorScopes, result.ChallengeScopes); err != nil {
			return err
		}
		return ErrRefreshReauthorization
	case ProbeInvalidToken:
		if _, err := refresh.Refresh(ctx, RefreshRequest{ServerID: serverID, ForceInvalidToken: true, ChallengeMetadata: result.ChallengeMetadata}); err != nil {
			return err
		}
		retried, err := probe.Probe(ctx)
		if err != nil || retried.Outcome != ProbeSucceeded {
			return ErrProbeRejected
		}
		return nil
	default:
		return ErrProbeRejected
	}
}
