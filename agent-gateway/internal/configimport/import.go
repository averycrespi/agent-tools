// Package configimport validates and stages portable configuration without a
// runtime, HTTP client, subprocess runner, OAuth owner or secret provider.
package configimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

var ErrInvalid = errors.New("configuration export is invalid or unsupported; use a complete version-1 export")

type Input struct {
	Document contract.ConfigExport
	Bytes    []byte
	Digest   string
}

func Read(reader io.Reader) (Input, error) {
	data, err := io.ReadAll(io.LimitReader(reader, contract.ConfigExportMaxBytes+1))
	if err != nil {
		return Input{}, fmt.Errorf("read configuration export: %w", err)
	}
	return Decode(data)
}

// Decode deliberately does not return parser/domain errors containing authored
// input. Ordinary configuration strings are not guaranteed secret-free.
func Decode(data []byte) (Input, error) {
	var value contract.ConfigExport
	if err := strictjson.Decode(data, &value, strictjson.Options{MaxBytes: contract.ConfigExportMaxBytes, MaxDepth: 64, RejectUnknownMembers: true}); err != nil {
		return Input{}, ErrInvalid
	}
	if !requiredShape(data, reflect.TypeFor[contract.ConfigExport]()) || contract.ValidateConfigExport(value) != nil || servers.ValidateImportedConfiguration(value.MCP.Servers) != nil {
		return Input{}, ErrInvalid
	}
	for _, server := range value.MCP.Servers {
		if server.Deleted {
			continue
		}
		transport, err := servers.DecodeTransport(server.Transport)
		if err != nil || !requiredShape(server.Transport, reflect.TypeOf(transport)) {
			return Input{}, ErrInvalid
		}
	}
	for _, c := range value.Git.Credentials {
		if _, err := gitcredentials.Normalize(c.GitCredentialDefinition); err != nil {
			return Input{}, ErrInvalid
		}
	}
	if authorization.ValidateImportedConfiguration(value) != nil {
		return Input{}, ErrInvalid
	}
	return Input{Document: value, Bytes: append([]byte(nil), data...), Digest: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

// requiredShape supplements strictjson's closed/duplicate-key checks with the
// format's required nullable members and nonnullable collection/scalar contract.
// Raw unions remain with their owning domain decoder, not reflection.
func requiredShape(raw json.RawMessage, typ reflect.Type) bool {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return len(raw) > 0
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return typ.Kind() == reflect.Pointer
	}
	if typ.Kind() == reflect.Pointer {
		return requiredShape(raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		declared := shapeFields(typ)
		for name := range fields {
			if _, ok := declared[name]; !ok {
				return false
			}
		}
		for name, field := range declared {
			tag := strings.Split(field.Tag.Get("json"), ",")
			member, ok := fields[name]
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if !ok {
				if optional {
					continue
				}
				return false
			}
			if optional && bytes.Equal(bytes.TrimSpace(member), []byte("null")) {
				return false
			}
			if !requiredShape(member, field.Type) {
				return false
			}
		}
	case reflect.Interface:
		var discriminator struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(raw, &discriminator) != nil {
			return false
		}
		if typ == reflect.TypeFor[contract.HTTPAuthentication]() {
			switch discriminator.Mode {
			case string(contract.AuthenticationNone):
				return requiredShape(raw, reflect.TypeFor[contract.NoAuthentication]())
			case string(contract.AuthenticationBearer):
				return requiredShape(raw, reflect.TypeFor[contract.BearerAuthentication]())
			case string(contract.AuthenticationOAuth):
				return requiredShape(raw, reflect.TypeFor[contract.OAuthAuthentication]())
			}
		}
		if typ == reflect.TypeFor[contract.OAuthRegistration]() {
			switch discriminator.Mode {
			case string(contract.RegistrationStatic):
				return requiredShape(raw, reflect.TypeFor[contract.StaticOAuthRegistration]())
			case string(contract.RegistrationDynamic):
				return requiredShape(raw, reflect.TypeFor[contract.DynamicOAuthRegistration]())
			}
		}
		return false
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !requiredShape(value, typ.Elem()) {
				return false
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !requiredShape(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}

func shapeFields(typ reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			for name, embedded := range shapeFields(field.Type) {
				fields[name] = embedded
			}
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "-" {
			fields[name] = field
		}
	}
	return fields
}

func ReceiptTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT input_digest FROM configuration_import WHERE singleton=1`).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return digest, err
}

// StageTx commits all domain definitions and a content-bound receipt in the
// caller's single atomic database transaction. A receipt is never replay input.
func StageTx(ctx context.Context, tx *sql.Tx, input Input, newID func() (string, error), now time.Time, targets authorization.StoredGrantTargetInspector) error {
	previous, err := ReceiptTx(ctx, tx)
	if err != nil {
		return err
	}
	if previous != "" {
		return ErrInvalid
	}
	ids := map[string]string{"mcp/" + contract.SyntheticServerNamespace: contract.SyntheticServerID}
	assign := func(ref string) error {
		id, err := newID()
		if err != nil {
			return err
		}
		if !contract.ValidAuditID(id) {
			return ErrInvalid
		}
		ids[ref] = id
		return nil
	}
	value := input.Document
	for _, a := range value.Agents {
		if err := assign(a.Ref); err != nil {
			return err
		}
	}
	for _, s := range value.MCP.Servers {
		if err := assign(s.Ref); err != nil {
			return err
		}
	}
	for _, c := range value.HTTP.Credentials {
		if err := assign(c.Ref); err != nil {
			return err
		}
	}
	for _, c := range value.Git.Credentials {
		if err := assign(c.Ref); err != nil {
			return err
		}
	}
	for _, r := range value.Git.Repositories {
		if err := assign(r.Ref); err != nil {
			return err
		}
	}
	if err := servers.StageImportedConfigurationTx(ctx, tx, value.MCP.Servers, ids, now); err != nil {
		return err
	}
	for _, c := range value.HTTP.Credentials {
		if err := httpcredentials.StageImportedDefinitionTx(ctx, tx, ids[c.Ref], httpcredentials.Definition{Name: c.Name, Boundary: httpcredentials.Boundary{Host: c.Boundary.Host, Port: c.Boundary.Port, AllowWildcard: c.Boundary.AllowWildcard}, Recipe: c.Recipe}, now); err != nil {
			return err
		}
	}
	for _, c := range value.Git.Credentials {
		if err := gitcredentials.StageImportedDefinitionTx(ctx, tx, ids[c.Ref], c.GitCredentialDefinition, now); err != nil {
			return err
		}
	}
	if err := authorization.StageImportedConfigurationTx(ctx, tx, value, ids, newID, now, targets); err != nil {
		return err
	}
	stagedDigest, err := stagedDigestTx(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO configuration_import(singleton,input_digest,staged_digest) VALUES(1,?,?)`, input.Digest, stagedDigest)
	return err
}
