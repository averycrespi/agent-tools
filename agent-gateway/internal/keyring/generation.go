package keyring

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

const (
	generationEntropyBytes = 32
	generationHandlePrefix = "mgw_kg1_"
)

var ErrIncompleteGeneration = errors.New("keyring generation is incomplete")
var secretMaximumBytes = int(mustFixedLimit("keyring_secret_bytes"))

type Handle string

func NewHandle(entropy io.Reader) (Handle, error) {
	value := make([]byte, generationEntropyBytes)
	if _, err := io.ReadFull(entropy, value); err != nil {
		return "", fmt.Errorf("generate keyring handle: %w", err)
	}
	return Handle(generationHandlePrefix + base64.RawURLEncoding.EncodeToString(value)), nil
}
func ParseHandle(value string) (Handle, error) {
	if !strings.HasPrefix(value, generationHandlePrefix) {
		return "", fmt.Errorf("invalid keyring handle")
	}
	encoded := strings.TrimPrefix(value, generationHandlePrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != generationEntropyBytes || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", fmt.Errorf("invalid keyring handle")
	}
	return Handle(value), nil
}
func (provider *Provider) WriteGeneration(ctx context.Context, namespace Namespace, handle Handle, secret []byte) error {
	if len(secret) == 0 {
		return fmt.Errorf("keyring secret is empty")
	}
	if len(secret) > secretMaximumBytes {
		return ErrSecretTooLarge
	}
	if err := provider.validateGeneration(namespace, handle); err != nil {
		return err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	return provider.custody.write(ctx, namespace, handle, secret)
}
func (provider *Provider) ReadGeneration(ctx context.Context, namespace Namespace, handle Handle) ([]byte, error) {
	if err := provider.validateGeneration(namespace, handle); err != nil {
		return nil, err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return nil, err
	}
	defer release()
	return provider.custody.read(ctx, namespace, handle)
}
func (provider *Provider) DeleteGeneration(ctx context.Context, namespace Namespace, handle Handle) error {
	if err := provider.validateGeneration(namespace, handle); err != nil {
		return err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	return provider.custody.remove(ctx, namespace, handle)
}
func (provider *Provider) validateGeneration(namespace Namespace, handle Handle) error {
	if _, err := ParseHandle(string(handle)); err != nil {
		return err
	}
	if err := provider.validate(namespace); err != nil {
		return err
	}
	if provider.custody == nil || provider.custody.capability().State != contract.KeyringReady {
		return ErrCustodyUnavailable
	}
	return nil
}
func mustFixedLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing fixed keyring limit: " + name)
	}
	return limit.Maximum
}
