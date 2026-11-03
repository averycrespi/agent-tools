package main

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

func credentialSetupFacts(ctx context.Context, tx *sql.Tx) ([]contract.CredentialSetupFact, error) {
	facts := []contract.CredentialSetupFact{}
	for _, read := range []func(context.Context, *sql.Tx) ([]contract.CredentialSetupFact, error){servers.SetupFactsTx, httpcredentials.SetupFactsTx, gitcredentials.SetupFactsTx} {
		values, err := read(ctx, tx)
		if err != nil {
			return nil, err
		}
		facts = append(facts, values...)
	}
	return facts, nil
}
