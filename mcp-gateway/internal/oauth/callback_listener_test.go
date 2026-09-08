package oauth

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type callbackAcquireFunc func(context.Context, string, string, time.Time) (func(), error)

func (acquire callbackAcquireFunc) AcquireCallback(ctx context.Context, id, uri string, expiry time.Time) (func(), error) {
	return acquire(ctx, id, uri, expiry)
}

func TestForegroundCallbackLeaseTerminalAndLatePreparationCleanup(t *testing.T) {
	for _, terminal := range []string{"cancel", "fence", "shutdown", "preparation failure", "late acquisition"} {
		t.Run(terminal, func(t *testing.T) {
			reserved, err := net.Listen("tcp4", "127.0.0.1:0")
			require.NoError(t, err)
			address := reserved.Addr().String()
			require.NoError(t, reserved.Close())
			callback := "http://" + address + "/callback"
			created := flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
			created.Configuration.Authentication.CallbackURI = &callback
			store := &flowStoreFake{created: created}
			registration := flowRegistration()
			registration.CallbackURL = callback
			registrar := &flowRegistrarFake{registration: registration}
			if terminal == "preparation failure" {
				registrar.err = errors.New("fixture registration rejected")
			}
			service := newFlowService(store, flowResolverFake{graph: Graph{Resource: created.Configuration.Resource, Issuer: registration.Issuer, AuthorizationEndpoint: "https://issuer.example/authorize"}}, registrar, io.LimitReader(zeroReader{}, 64), "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
			var listener net.Listener
			released := 0
			service.listeners = callbackAcquireFunc(func(ctx context.Context, id, uri string, expiry time.Time) (func(), error) {
				assert.Equal(t, created.Flow.ID, id)
				assert.Equal(t, callback, uri)
				assert.True(t, expiry.After(flowTime))
				config := net.ListenConfig{}
				var err error
				listener, err = config.Listen(ctx, "tcp", address)
				if err != nil {
					return nil, err
				}
				if terminal == "late acquisition" {
					service.Shutdown()
				}
				return func() { released++; _ = listener.Close() }, nil
			})
			t.Cleanup(func() {
				service.Shutdown()
				if listener != nil {
					_ = listener.Close()
				}
			})
			flow, err := service.Create(context.Background(), FlowRequest{ServerID: created.Flow.ServerID, ExpectedDesiredRevision: "1"})
			if terminal == "preparation failure" || terminal == "late acquisition" {
				require.Error(t, err)
				assert.Empty(t, flow.AuthorizationURL)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, flow.AuthorizationURL)
				occupied, err := net.Listen("tcp", address)
				if occupied != nil {
					_ = occupied.Close()
				}
				require.Error(t, err, "callback must be acquired before URL publication")
				switch terminal {
				case "cancel":
					_, err = service.Cancel(context.Background(), created.Flow.ServerID, flow.Flow.ID)
					require.NoError(t, err)
				case "fence":
					service.FenceServer(created.Flow.ServerID)
				case "shutdown":
					service.Shutdown()
				}
			}
			assert.Equal(t, 1, released)
			assert.Empty(t, service.byState)
			assert.Empty(t, service.leases)
			rebound, err := net.Listen("tcp", address)
			require.NoError(t, err)
			require.NoError(t, rebound.Close())
		})
	}
}
