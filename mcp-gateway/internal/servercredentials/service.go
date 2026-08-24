package servercredentials

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const staticGenerationVersion = 1

var ErrInvalidSecret = errors.New("credential replacement secret is invalid")

type Store interface {
	PrepareCredentialReplacement(context.Context, servers.CredentialReplacementRequest) (servers.CredentialReplacementPlan, error)
	CredentialReplacementCallback(servers.CredentialReplacementPlan, func()) (keyring.AuthorityCallback, func() (servers.CredentialReplacementPublication, bool))
}

type Coordinator interface {
	ReplaceFenced(context.Context, keyring.Namespace, []byte, keyring.AuthorityCallback) (keyring.CutoverResult, error)
}

type Service struct {
	store          Store
	coordinator    Coordinator
	installationID string
	fence          func(string)
	recover        func(string)
}

type StaticGeneration struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

func New(store Store, coordinator Coordinator, installationID string, fence, recoverRuntime func(string)) (*Service, error) {
	if store == nil || coordinator == nil || installationID == "" {
		return nil, ErrInvalidSecret
	}
	if fence == nil {
		fence = func(string) {}
	}
	if recoverRuntime == nil {
		recoverRuntime = func(string) {}
	}
	return &Service{store: store, coordinator: coordinator, installationID: installationID, fence: fence, recover: recoverRuntime}, nil
}

func (service *Service) Prepare(ctx context.Context, request servers.CredentialReplacementRequest) (servers.CredentialReplacementPlan, error) {
	return service.store.PrepareCredentialReplacement(ctx, request)
}

func (service *Service) Replace(ctx context.Context, plan servers.CredentialReplacementPlan, secret []byte) (servers.CredentialReplacementPublication, error) {
	defer clear(secret)
	recordKind := keyring.RecordStaticCredential
	if plan.Fence.Kind == contract.ServerCredentialOAuthClient {
		recordKind = keyring.RecordOAuthClient
	}
	namespace, err := keyring.NewNamespace(service.installationID, plan.Fence.ServerID, recordKind)
	if err != nil {
		return servers.CredentialReplacementPublication{}, ErrInvalidSecret
	}
	fenced := false
	callback, result := service.store.CredentialReplacementCallback(plan, func() {
		service.fence(plan.Fence.ServerID)
		fenced = true
	})
	_, replaceErr := service.coordinator.ReplaceFenced(ctx, namespace, secret, callback)
	publication, committed := result()
	if committed {
		return publication, replaceErr
	}
	if fenced {
		service.recover(plan.Fence.ServerID)
	}
	if replaceErr == nil {
		replaceErr = ErrInvalidSecret
	}
	return servers.CredentialReplacementPublication{}, replaceErr
}

func EncodeStaticGeneration(values map[string]string) ([]byte, error) {
	if len(values) == 0 {
		return nil, ErrInvalidSecret
	}
	copyValues := make(map[string]string, len(values))
	for slot, value := range values {
		if slot == "" || value == "" || !utf8.ValidString(value) {
			return nil, ErrInvalidSecret
		}
		copyValues[slot] = value
	}
	encoded, err := json.Marshal(StaticGeneration{Version: staticGenerationVersion, Values: copyValues})
	if err != nil || !limit("keyring_secret_bytes").Allows(int64(len(encoded))) {
		return nil, ErrInvalidSecret
	}
	return encoded, nil
}

func DecodeStaticGeneration(encoded []byte) (StaticGeneration, error) {
	var generation StaticGeneration
	if strictjson.Decode(encoded, &generation, strictjson.Options{MaxBytes: limit("keyring_secret_bytes").Maximum, MaxDepth: int(limit("json_depth").Maximum), RejectUnknownMembers: true}) != nil || generation.Version != staticGenerationVersion {
		return StaticGeneration{}, ErrInvalidSecret
	}
	if _, err := EncodeStaticGeneration(generation.Values); err != nil {
		return StaticGeneration{}, err
	}
	return generation, nil
}

func limit(name string) contract.FixedLimit {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing credential limit: " + name)
	}
	return value
}
