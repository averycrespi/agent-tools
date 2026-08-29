package grantrequests

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescriptorEvidenceIsCanonicalBoundedAndCopySafe(t *testing.T) {
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: "echo", ExternalName: "sample.echo",
		Descriptor: json.RawMessage(`{"name":"echo","inputSchema":{"type":"object"}}`),
	}, catalog.NormalizeOptions{ServerID: "01J60000000000000000000040", AllowHeaderBindings: true})
	require.NoError(t, err)
	source := catalog.DurableDescriptor{State: contract.EvidenceCurrent, Resource: contract.ToolDescriptor{
		ID: "01J60000000000000000000041", ServerID: normalized.Key.ServerID,
		UpstreamName: normalized.Key.UpstreamName, ExternalName: normalized.ExternalName,
		Descriptor: normalized.Descriptor, Fingerprint: normalized.Fingerprint, CatalogRevision: "1",
	}}
	capturedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	descriptorLimit, ok := contract.FixedLimitByName("tool_descriptor_bytes")
	require.True(t, ok)
	assert.Equal(t, int64(131072), descriptorLimit.Maximum)
	evidenceLimit, ok := contract.FixedLimitByName("grant_request_evidence_snapshot_bytes")
	require.True(t, ok)
	assert.Equal(t, int64(135168), evidenceLimit.Maximum)

	evidence, canonical, err := BuildDescriptorEvidence(source, "sample", capturedAt)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(canonical), 135168)
	assert.Equal(t, contract.EvidenceCurrent, evidence.DurableState)
	assert.Equal(t, "2026-08-27T12:00:00.000000000Z", evidence.CapturedAt)
	assert.JSONEq(t, string(canonical), string(mustJSON(t, evidence)))

	evidence.Descriptor.InputSchema[0] = 'x'
	fresh, _, err := BuildDescriptorEvidence(source, "sample", capturedAt)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(fresh.Descriptor.InputSchema))

	source.Resource.Fingerprint = "changed"
	_, _, err = BuildDescriptorEvidence(source, "sample", capturedAt)
	require.ErrorIs(t, err, ErrInvalidEvidence)
	source.Resource.Fingerprint = normalized.Fingerprint
	source.Resource.ExternalName = string(make([]byte, 129))
	_, _, err = BuildDescriptorEvidence(source, "sample", capturedAt)
	require.ErrorIs(t, err, ErrInvalidEvidence)
}

func TestPolicyValidationAndDurationConversion(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy contract.Policy
		valid  bool
	}{
		{name: "permanent server", policy: policy(contract.PolicyServer, "sample", nil, nil, true), valid: true},
		{name: "minimum tool", policy: policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/x":1}}`), stringPointer("60"), false), valid: true},
		{name: "maximum tool", policy: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("2592000"), false), valid: true},
		{name: "server constraint", policy: policy(contract.PolicyServer, "sample", constraint(`{"equals":{"/x":1}}`), nil, true)},
		{name: "server acknowledgement", policy: policy(contract.PolicyServer, "sample", nil, nil, false)},
		{name: "tool acknowledgement", policy: policy(contract.PolicyTool, "sample.echo", nil, nil, true)},
		{name: "empty target", policy: policy(contract.PolicyTool, "", nil, nil, false)},
		{name: "too long target", policy: policy(contract.PolicyTool, string(make([]byte, 129)), nil, nil, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := CompilePolicy(test.policy)
			if !test.valid {
				require.ErrorIs(t, err, ErrInvalidPolicy)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.policy.Scope, compiled.Scope())
			assert.Equal(t, test.policy.Target, compiled.Target())
		})
	}
	for _, invalid := range []string{"", "0", "59", "060", "+60", "-60", "60.0", "6e1", " 60", "60 ", "2592001"} {
		_, err := CompilePolicy(policy(contract.PolicyTool, "sample.echo", nil, &invalid, false))
		require.ErrorIs(t, err, ErrInvalidPolicy, invalid)
	}

	approved, err := CompilePolicy(policy(contract.PolicyTool, "sample.echo", nil, stringPointer("60"), false))
	require.NoError(t, err)
	at := time.Date(2026, 8, 27, 12, 0, 0, 123, time.FixedZone("offset", 3600))
	expires := approved.ExpiresAt(at)
	require.NotNil(t, expires)
	assert.Equal(t, time.Date(2026, 8, 27, 11, 1, 0, 123, time.UTC), *expires)
	permanent, err := CompilePolicy(policy(contract.PolicyServer, "sample", nil, nil, true))
	require.NoError(t, err)
	assert.Nil(t, permanent.ExpiresAt(at))
}

func TestCanonicalDedupePreservesLexicalIdentityAndNormalizesAtomOrder(t *testing.T) {
	target := ResolvedTarget{ServerID: "01J60000000000000000000040", UpstreamName: stringPointer("echo")}
	left := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/b":true,"/a":1.0}}`), stringPointer("60"), false))
	right := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/a":1.0,"/b":true}}`), stringPointer("60"), false))
	leftIdentity, err := CanonicalDedupeIdentity(left, target)
	require.NoError(t, err)
	rightIdentity, err := CanonicalDedupeIdentity(right, target)
	require.NoError(t, err)
	assert.Equal(t, int64(1), leftIdentity.Version)
	assert.Equal(t, leftIdentity.Bytes, rightIdentity.Bytes)

	escaped := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/\u0078":"\u0061"}}`), nil, false))
	decoded := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/x":"a"}}`), nil, false))
	escapedIdentity, err := CanonicalDedupeIdentity(escaped, target)
	require.NoError(t, err)
	decodedIdentity, err := CanonicalDedupeIdentity(decoded, target)
	require.NoError(t, err)
	assert.Equal(t, escapedIdentity.Bytes, decodedIdentity.Bytes)

	for _, token := range []string{"1", "1.0", "1e0"} {
		compiled := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/x":`+token+`}}`), nil, false))
		identity, identityErr := CanonicalDedupeIdentity(compiled, target)
		require.NoError(t, identityErr)
		if token != "1" {
			one := mustCompilePolicy(t, policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/x":1}}`), nil, false))
			oneIdentity, oneErr := CanonicalDedupeIdentity(one, target)
			require.NoError(t, oneErr)
			assert.NotEqual(t, oneIdentity.Bytes, identity.Bytes)
		}
	}

	changedTarget, err := CanonicalDedupeIdentity(left, ResolvedTarget{ServerID: "01J60000000000000000000041", UpstreamName: stringPointer("echo")})
	require.NoError(t, err)
	assert.NotEqual(t, leftIdentity.Bytes, changedTarget.Bytes)
	serverTarget, err := CanonicalDedupeIdentity(mustCompilePolicy(t, policy(contract.PolicyServer, "sample", nil, nil, true)), ResolvedTarget{ServerID: target.ServerID})
	require.NoError(t, err)
	assert.NotEqual(t, leftIdentity.Bytes, serverTarget.Bytes)
}

func TestPolicyNarrowingMatrix(t *testing.T) {
	server := ResolvedTarget{ServerID: "01J60000000000000000000040"}
	tool := ResolvedTarget{ServerID: server.ServerID, UpstreamName: stringPointer("echo")}
	otherTool := ResolvedTarget{ServerID: server.ServerID, UpstreamName: stringPointer("other")}
	otherServer := ResolvedTarget{ServerID: "01J60000000000000000000041"}
	baseConstraint := constraint(`{"equals":{"/x":1,"/y":"a"}}`)
	moreConstraint := constraint(`{"equals":{"/z":true,"/y":"a","/x":1}}`)
	missingConstraint := constraint(`{"equals":{"/x":1}}`)
	lexicalChange := constraint(`{"equals":{"/x":1.0,"/y":"a"}}`)

	tests := []struct {
		name      string
		submitted contract.Policy
		subTarget ResolvedTarget
		approved  contract.Policy
		appTarget ResolvedTarget
		valid     bool
	}{
		{name: "server unchanged", submitted: policy(contract.PolicyServer, "sample", nil, nil, true), subTarget: server, approved: policy(contract.PolicyServer, "sample", nil, nil, true), appTarget: server, valid: true},
		{name: "server to exact", submitted: policy(contract.PolicyServer, "sample", nil, nil, true), subTarget: server, approved: policy(contract.PolicyTool, "sample.echo", constraint(`{"equals":{"/x":1}}`), stringPointer("60"), false), appTarget: tool, valid: true},
		{name: "server changed", submitted: policy(contract.PolicyServer, "sample", nil, nil, true), subTarget: server, approved: policy(contract.PolicyServer, "other", nil, nil, true), appTarget: otherServer},
		{name: "tool unchanged", submitted: policy(contract.PolicyTool, "sample.echo", nil, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", nil, nil, false), appTarget: tool, valid: true},
		{name: "tool adds constraint", submitted: policy(contract.PolicyTool, "sample.echo", nil, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", baseConstraint, nil, false), appTarget: tool, valid: true},
		{name: "tool changes identity", submitted: policy(contract.PolicyTool, "sample.echo", nil, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.other", nil, nil, false), appTarget: otherTool},
		{name: "tool broadens to server", submitted: policy(contract.PolicyTool, "sample.echo", nil, nil, false), subTarget: tool, approved: policy(contract.PolicyServer, "sample", nil, nil, true), appTarget: server},
		{name: "retains and adds atoms", submitted: policy(contract.PolicyTool, "sample.echo", baseConstraint, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", moreConstraint, nil, false), appTarget: tool, valid: true},
		{name: "drops atom", submitted: policy(contract.PolicyTool, "sample.echo", baseConstraint, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", missingConstraint, nil, false), appTarget: tool},
		{name: "changes lexical number", submitted: policy(contract.PolicyTool, "sample.echo", baseConstraint, nil, false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", lexicalChange, nil, false), appTarget: tool},
		{name: "temporary shortens", submitted: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("61"), false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("60"), false), appTarget: tool, valid: true},
		{name: "temporary lengthens", submitted: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("60"), false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("61"), false), appTarget: tool},
		{name: "temporary becomes permanent", submitted: policy(contract.PolicyTool, "sample.echo", nil, stringPointer("60"), false), subTarget: tool, approved: policy(contract.PolicyTool, "sample.echo", nil, nil, false), appTarget: tool},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			submitted := mustCompilePolicy(t, test.submitted)
			approved := mustCompilePolicy(t, test.approved)
			err := ValidateNarrowing(submitted, test.subTarget, approved, test.appTarget)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrPolicyBroadening)
			}
		})
	}
}

func policy(scope contract.PolicyScope, target string, value *json.RawMessage, duration *string, acknowledged bool) contract.Policy {
	return contract.Policy{Scope: scope, Target: target, Constraint: value, DurationSeconds: duration, FutureToolsAcknowledged: acknowledged}
}

func constraint(value string) *json.RawMessage {
	raw := json.RawMessage(value)
	return &raw
}

func stringPointer(value string) *string { return &value }

func mustCompilePolicy(t *testing.T, value contract.Policy) CompiledPolicy {
	t.Helper()
	compiled, err := CompilePolicy(value)
	require.NoError(t, err)
	return compiled
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	contents, err := json.Marshal(value)
	require.NoError(t, err)
	return contents
}
