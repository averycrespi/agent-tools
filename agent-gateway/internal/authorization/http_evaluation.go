package authorization

import (
	"context"
	"database/sql"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// HTTPAccessInput is transient. Neither it nor its coordinates enter evidence,
// audit, invalidations or retained authority. CONNECT uses Host and Port only.
type HTTPAccessInput struct {
	PrincipalID string                            `json:"principal_id"`
	URL         string                            `json:"url,omitempty"`
	Method      string                            `json:"method,omitempty"`
	Connect     *contract.HTTPDestinationSelector `json:"connect,omitempty"`
}

type httpTarget struct {
	request     httppolicy.Request
	destination httppolicy.Destination
	connect     bool
}

func parseHTTPTarget(in HTTPAccessInput) (httpTarget, error) {
	if !validOpaqueID(in.PrincipalID) {
		return httpTarget{}, ErrInvalidInput
	}
	if in.Connect != nil {
		if in.URL != "" || in.Method != "" {
			return httpTarget{}, ErrInvalidInput
		}
		d, err := httppolicy.NewDestination(in.Connect.Host, in.Connect.Port)
		if err != nil {
			return httpTarget{}, ErrInvalidInput
		}
		return httpTarget{destination: d, connect: true}, nil
	}
	if len(in.URL) > contract.HTTPTargetBytes {
		return httpTarget{}, ErrInvalidInput
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return httpTarget{}, ErrInvalidInput
	}
	req, err := httppolicy.ParseRequest(in.URL, in.Method, u.Host, "", nil)
	if err != nil {
		return httpTarget{}, ErrInvalidInput
	}
	return httpTarget{request: req, destination: req.Destination()}, nil
}

func httpEvaluatorTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) (*httppolicy.Evaluator, contract.HTTPDefault, error) {
	principal, err := principalByIDTx(ctx, tx, id)
	if err != nil {
		return nil, "", err
	}
	if principal.State != contract.PrincipalActive {
		return nil, "", ErrAuthorizationUnavailable
	}
	def, err := httpDefaultTx(ctx, tx, id)
	if err != nil {
		return nil, "", err
	}
	principalRevision, err := httpRevision(principal.Revision)
	if err != nil {
		return nil, "", err
	}
	defaultRevision, err := httpRevision(def.Revision)
	if err != nil {
		return nil, "", err
	}
	revision, err := authorizationRevisionTx(ctx, tx)
	if err != nil {
		return nil, "", err
	}
	policyRevision, err := strconv.ParseUint(revision, 10, 64)
	if err != nil {
		return nil, "", ErrInvalidState
	}
	// The shared sequence starts at zero. HTTP's positive evidence representation
	// is that same sequence plus one, not an independently advancing authority.
	snapshot := httppolicy.Snapshot{Principal: contract.HTTPRevisionRef{ID: id, Revision: principalRevision}, Default: def.Default, DefaultRevision: defaultRevision, PolicyRevision: policyRevision + 1}
	grants, err := readHTTPGrantsTx(ctx, tx, now)
	if err != nil {
		return nil, "", err
	}
	credentials := map[string]bool{}
	for _, g := range grants {
		// Validate every retained reference, including expired or foreign grants.
		if err := checkHTTPReferenceTx(ctx, tx, g.policy); err != nil {
			return nil, "", ErrInvalidState
		}
		if g.resource.PrincipalID != id || g.resource.State != contract.GrantActive {
			continue
		}
		grantRevision, err := httpRevision(g.resource.Revision)
		if err != nil {
			return nil, "", err
		}
		snapshot.Grants = append(snapshot.Grants, httppolicy.Grant{Ref: contract.HTTPRevisionRef{ID: g.resource.ID, Revision: grantRevision}, PrincipalID: id, Policy: g.policy})
		credentialID := g.policy.CredentialID()
		if credentialID != "" && !credentials[credentialID] {
			credential, err := httpcredentials.PolicyCredentialTx(ctx, tx, credentialID)
			if err != nil {
				return nil, "", ErrInvalidState
			}
			snapshot.Credentials = append(snapshot.Credentials, credential)
			credentials[credentialID] = true
		}
	}
	evaluator, err := httppolicy.New(snapshot)
	if err != nil {
		return nil, "", ErrInvalidState
	}
	return evaluator, def.Default, nil
}

func (r *Repository) PreviewHTTPAccess(ctx context.Context, in HTTPAccessInput) (out contract.HTTPAccessPreview, err error) {
	target, err := parseHTTPTarget(in)
	if err != nil {
		return out, err
	}
	err = r.view(ctx, func(tx *sql.Tx) error {
		evaluator, def, err := httpEvaluatorTx(ctx, tx, in.PrincipalID, r.clock.Now())
		if err != nil {
			return err
		}
		if target.connect {
			out.Decision, err = evaluator.ConnectPolicy(target.destination)
		} else {
			out.Decision, err = evaluator.RequestPolicy(target.request)
		}
		if err != nil {
			return err
		}
		// Literal classification is pure; DNS answers and Gateway aliases remain
		// deliberately unverified. Never manufacture address facts for a hostname.
		if ip, err := netip.ParseAddr(target.destination.Host()); err == nil && out.Decision.Allowed {
			switch httppolicy.ClassifyAddress(ip) {
			case httppolicy.AddressForbidden:
				out.Decision.Allowed = false
				out.Decision.Reason = contract.HTTPReasonAddressForbidden
			case httppolicy.AddressPrivate:
				if out.Decision.PrivateGrant == nil {
					out.Decision.Allowed = false
					out.Decision.Reason = contract.HTTPReasonPrivateRequired
				}
			}
		}
		out.Default = def
		out.PolicyOnly = true
		return nil
	})
	return
}

// EvaluateHTTPAccess is the ingress-facing evaluation seam for the singular
// authenticated agent lease. The result is evidence, not admission or dispatch
// authority; a future ingress must seal and confirm its own persisted receipt.
func (r *Repository) EvaluateHTTPAccess(ctx context.Context, lease *Lease, in HTTPAccessInput, facts httppolicy.AddressFacts) (out contract.HTTPDecision, err error) {
	target, err := parseHTTPTarget(in)
	if err != nil {
		return out, err
	}
	err = r.WithAdmission(ctx, lease, func(admission *Admission) error {
		return r.view(ctx, func(tx *sql.Tx) error {
			if in.PrincipalID != lease.binding.PrincipalID {
				return ErrAuthenticationRequired
			}
			if _, err := admission.VerifyBindingOnlyTx(ctx, tx); err != nil {
				return err
			}
			evaluator, _, err := httpEvaluatorTx(ctx, tx, in.PrincipalID, r.clock.Now())
			if err != nil {
				return err
			}
			if target.connect {
				out, err = evaluator.Connect(target.destination, facts)
			} else {
				out, err = evaluator.Request(target.request, facts)
			}
			return err
		})
	})
	return
}
