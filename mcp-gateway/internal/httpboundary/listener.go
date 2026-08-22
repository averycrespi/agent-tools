package httpboundary

import (
	"context"
	"fmt"
	"net"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type KeyringProbe func(context.Context) (contract.KeyringCapability, error)

func OpenListener(ctx context.Context, authority string, probe KeyringProbe) (net.Listener, contract.KeyringCapability, error) {
	return openListener(ctx, authority, probe, net.Listen)
}

func openListener(
	ctx context.Context,
	authority string,
	probe KeyringProbe,
	listen func(network, address string) (net.Listener, error),
) (net.Listener, contract.KeyringCapability, error) {
	if err := ValidateAuthority(authority); err != nil {
		return nil, "", err
	}
	capability := contract.KeyringUnsupported
	if probe != nil {
		var err error
		capability, err = probe(ctx)
		if err != nil {
			return nil, capability, fmt.Errorf("probe keyring capability: %w", err)
		}
	}
	listener, err := listen("tcp4", authority)
	if err != nil {
		return nil, capability, fmt.Errorf("listen on configured authority: %w", err)
	}
	return listener, capability, nil
}
