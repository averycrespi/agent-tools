// Package configexport materializes version-two portable configuration from
// owner-local projections in one snapshot, without a material or network seam.
package configexport

import "github.com/averycrespi/agent-tools/agent-gateway/internal/storage"

type Service struct{ store *storage.Store }

func New(store *storage.Store) *Service { return &Service{store: store} }
