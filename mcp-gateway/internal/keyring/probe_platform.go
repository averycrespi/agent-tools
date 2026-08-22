package keyring

import (
	"context"
	"errors"
)

const (
	securityItemNotFoundExit = 44
	securityAuthFailedExit   = 51
)

var (
	errSecurityToolMissing    = errors.New("security tool is missing")
	errSecretCollectionAbsent = errors.New("default secret collection is absent")
)

type securityProbeRunner interface {
	Run(context.Context, ...string) error
}

func classifySecurityExit(operation string, exitCode int) error {
	switch {
	case operation == "default-keychain" && exitCode == securityItemNotFoundExit:
		return errBackendAbsent
	case operation == "show-keychain-info" && exitCode == securityAuthFailedExit:
		return errBackendLocked
	default:
		return errors.New("security metadata probe failed")
	}
}

func probeDarwin(ctx context.Context, runner securityProbeRunner) error {
	if err := runner.Run(ctx, "default-keychain", "-d", "user"); err != nil {
		return err
	}
	return runner.Run(ctx, "show-keychain-info")
}

type linuxProbeClient interface {
	HasOwner(context.Context) (bool, error)
	Locked(context.Context) (bool, error)
	UnlockWithoutPrompt(context.Context) (unlocked bool, prompt bool, err error)
	DismissPrompt(context.Context) error
	Close() error
}

type linuxProbeConnector func() (linuxProbeClient, error)

func probeLinux(ctx context.Context, sessionAddress string, connect linuxProbeConnector) error {
	if sessionAddress == "" {
		return errBackendAbsent
	}
	client, err := connect()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	hasOwner, err := client.HasOwner(ctx)
	if err != nil {
		return err
	}
	if !hasOwner {
		return errBackendAbsent
	}
	locked, err := client.Locked(ctx)
	if err != nil {
		if errors.Is(err, errSecretCollectionAbsent) {
			return errBackendAbsent
		}
		return err
	}
	if !locked {
		return nil
	}
	unlocked, prompt, err := client.UnlockWithoutPrompt(ctx)
	if err != nil {
		return err
	}
	if unlocked {
		return nil
	}
	if prompt {
		if err := client.DismissPrompt(ctx); err != nil {
			return err
		}
		return errBackendInteractionRequired
	}
	return errBackendLocked
}
