//go:build integration

package keyring

import "context"

// ObserveCustodyForIntegration injects failures and completion barriers around the
// real encrypted generation owner. It is absent from production and E2E builds;
// it neither supplies alternate custody nor exposes protected bytes.
func ObserveCustodyForIntegration(provider *Provider, observe func(string) error) {
	provider.custody = &observedCustody{delegate: provider.custody, observe: observe}
}

type observedCustody struct {
	delegate generationCustody
	observe  func(string) error
}

func (c *observedCustody) capability() Capability { return c.delegate.capability() }
func (c *observedCustody) write(ctx context.Context, ns Namespace, h Handle, value []byte) error {
	if err := c.observe("before_write"); err != nil {
		return err
	}
	if err := c.delegate.write(ctx, ns, h, value); err != nil {
		return err
	}
	return c.observe("after_write")
}
func (c *observedCustody) read(ctx context.Context, ns Namespace, h Handle) ([]byte, error) {
	if err := c.observe("before_read"); err != nil {
		return nil, err
	}
	return c.delegate.read(ctx, ns, h)
}
func (c *observedCustody) remove(ctx context.Context, ns Namespace, h Handle) error {
	if err := c.observe("before_delete"); err != nil {
		return err
	}
	return c.delegate.remove(ctx, ns, h)
}
