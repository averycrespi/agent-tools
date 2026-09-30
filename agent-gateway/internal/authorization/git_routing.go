package authorization

import (
	"context"
	"database/sql"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

func gitLocators(repositories []storedGitRepository) []string {
	locators := make([]string, 0, len(repositories)*2)
	for _, repository := range repositories {
		locators = append(locators, repository.resource.URL)
		locators = append(locators, repository.resource.Aliases...)
	}
	return locators
}

// ResolveGitRequest selects a coherent profile/repository snapshot before any
// body reads. Its revisions must still match at evaluation and confirmation.
func (r *Repository) ResolveGitRequest(ctx context.Context, target httppolicy.Request) (repository contract.GitRepository, profile contract.GitRoutingProfile, git bool, err error) {
	err = r.view(ctx, func(tx *sql.Tx) error {
		var e error
		profile, e = gitRoutingProfileTx(ctx, tx)
		if e != nil {
			return e
		}
		if !slices.Contains(profile.Origins, target.Scheme()+"://"+target.Destination().Authority()) {
			return nil
		}
		repositories, e := readGitRepositoriesTx(ctx, tx)
		if e != nil {
			return e
		}
		base, _, shaped, e := gitwire.Route(target, gitLocators(repositories)...)
		git = shaped
		if !shaped {
			return nil
		}
		if e != nil {
			return ErrInvalidInput
		}
		for _, candidate := range repositories {
			if candidate.deleted {
				continue
			}
			if base == candidate.resource.URL || slices.Contains(candidate.resource.Aliases, base) {
				repository = candidate.resource
				return nil
			}
		}
		return ErrNotFound
	})
	return
}
