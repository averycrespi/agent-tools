//go:build !e2e

package httpproxy

import (
	"crypto/x509"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type fixtureTrust struct{ roots *x509.CertPool }

func (e *Engine) loadFixtureTrust() error { return nil }

func (e *Engine) upstreamRoots(_ httppolicy.Request) *x509.CertPool { return e.roots }
