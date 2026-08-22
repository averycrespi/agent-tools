//go:build !darwin && !linux

package keyring

import (
	"context"

	oskeyring "github.com/zalando/go-keyring"
)

func probeSystem(context.Context, string) error {
	return oskeyring.ErrUnsupportedPlatform
}
