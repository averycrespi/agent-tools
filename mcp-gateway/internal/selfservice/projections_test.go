package selfservice

import (
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
)

func TestS5SelfProjectionBoundaryHasNoCrossPrincipalSelectorOrForbiddenFields(t *testing.T) {
	reader := reflect.TypeOf((*ProjectionReader)(nil)).Elem()
	identity, found := reader.MethodByName("ReadSelfIdentity")
	if assert.True(t, found) {
		assert.Equal(t, 2, identity.Type.NumIn())
		assert.Equal(t, reflect.TypeOf(authorization.AdmittedSubject{}), identity.Type.In(1))
		assert.Equal(t, reflect.TypeOf(contract.SelfIdentity{}), identity.Type.Out(0))
	}
	grants, found := reader.MethodByName("ListSelfGrants")
	if assert.True(t, found) {
		assert.Equal(t, 4, grants.Type.NumIn())
		assert.Equal(t, reflect.TypeOf(authorization.AdmittedSubject{}), grants.Type.In(1))
		assert.Equal(t, reflect.TypeOf((*authorization.SelfGrantCursor)(nil)), grants.Type.In(2))
	}
	assert.Equal(t, 2, reader.NumMethod())

	subject := reflect.TypeOf(authorization.AdmittedSubject{})
	assert.Equal(t, 6, subject.NumField())
	for index := 0; index < subject.NumField(); index++ {
		name := strings.ToLower(subject.Field(index).Name)
		for _, forbidden := range []string{"bearer", "verifier", "fingerprint", "visibility"} {
			assert.NotContains(t, name, forbidden)
		}
	}

	for _, resource := range []any{contract.SelfIdentity{}, contract.AgentGrant{}, contract.GrantPolicy{}} {
		typeOf := reflect.TypeOf(resource)
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			assert.NotContains(t, []string{
				"principal_id", "server_id", "upstream_name", "credential_id", "credential_fingerprint",
				"credential_verifier", "authorization_revision", "authorization_evidence",
			}, field.Tag.Get("json"))
		}
	}
}
