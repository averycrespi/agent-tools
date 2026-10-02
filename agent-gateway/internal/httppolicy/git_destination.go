package httppolicy

import "github.com/averycrespi/agent-tools/agent-gateway/internal/contract"

// GitDestination applies only shared destination exclusions and explicit private
// network permission. It confers no Git repository/ref or HTTP request authority,
// and never selects an HTTP credential or default allow.
func (e *Evaluator) GitDestination(r Request, facts AddressFacts) (allowed bool, private *contract.HTTPRevisionRef, err error) {
	if e == nil || r.destination.host == "" {
		return false, nil, ErrInvalid
	}
	if e.destinationBlock(r.destination) != nil {
		return false, nil, nil
	}
	for _, g := range e.grants {
		if g.Policy.kind == contract.HTTPAllowRequests && g.Policy.private && g.Policy.matchesRequest(r) {
			ref := g.Ref
			private = &ref
			break
		}
	}
	reason, err := checkAddresses(r.destination, facts, private != nil)
	return err == nil && reason == "", private, err
}
