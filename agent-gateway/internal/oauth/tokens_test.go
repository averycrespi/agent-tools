package oauth

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenResponseStrictMatrixAndScopeSemantics(t *testing.T) {
	validHeader := http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}
	tests := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		requested []string
		wantErr   bool
		wantScope []string
		specified bool
	}{
		{name: "valid extension and narrowing", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"bearer","scope":"alpha","extension":{"ignored":true}}`, requested: []string{"alpha", "beta"}, wantScope: []string{"alpha"}, specified: true},
		{name: "requested omission", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer"}`, requested: []string{"alpha"}, wantScope: []string{"alpha"}, specified: true},
		{name: "default omission", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer"}`, wantScope: nil, specified: false},
		{name: "default returned", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer","scope":"zeta alpha"}`, wantScope: []string{"alpha", "zeta"}, specified: true},
		{name: "wrong status", status: 201, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer"}`, wantErr: true},
		{name: "wrong media", status: 200, header: http.Header{"Content-Type": []string{"text/plain"}}, body: `{"access_token":"opaque","token_type":"Bearer"}`, wantErr: true},
		{name: "duplicate", status: 200, header: validHeader, body: `{"access_token":"one","access_token":"two","token_type":"Bearer"}`, wantErr: true},
		{name: "empty access", status: 200, header: validHeader, body: `{"access_token":"","token_type":"Bearer"}`, wantErr: true},
		{name: "wrong type", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"DPoP"}`, wantErr: true},
		{name: "negative expiry", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer","expires_in":-1}`, wantErr: true},
		{name: "fraction expiry", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer","expires_in":1.5}`, wantErr: true},
		{name: "scope expansion", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer","scope":"alpha gamma"}`, requested: []string{"alpha", "beta"}, wantErr: true},
		{name: "scope spacing", status: 200, header: validHeader, body: `{"access_token":"opaque","token_type":"Bearer","scope":"alpha  beta"}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseTokenResponse(test.status, test.header, []byte(test.body), test.requested, flowTime)
			if test.wantErr {
				assert.ErrorIs(t, err, ErrTokenRejected)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantScope, parsed.scopes)
			assert.Equal(t, test.specified, parsed.scopeSpecified)
		})
	}
}

func TestTokenRefreshEligibilityUsesBoundedLifetimeLead(t *testing.T) {
	issued := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		lifetime time.Duration
		before   time.Duration
		want     bool
	}{
		{name: "five second floor before", lifetime: 20 * time.Second, before: 6 * time.Second},
		{name: "five second floor boundary", lifetime: 20 * time.Second, before: 5 * time.Second, want: true},
		{name: "ten percent boundary", lifetime: 5 * time.Minute, before: 30 * time.Second, want: true},
		{name: "sixty second cap before", lifetime: 2 * time.Hour, before: 61 * time.Second},
		{name: "sixty second cap boundary", lifetime: 2 * time.Hour, before: 60 * time.Second, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			expires := issued.Add(test.lifetime).Format(time.RFC3339Nano)
			generation := TokenGeneration{IssuedAt: issued.Format(time.RFC3339Nano), ExpiresAt: &expires}
			assert.Equal(t, test.want, TokenRefreshEligible(generation, issued.Add(test.lifetime-test.before)))
		})
	}
	assert.False(t, TokenRefreshEligible(TokenGeneration{IssuedAt: issued.Format(time.RFC3339Nano)}, issued.Add(time.Hour)))
}

func TestTokenGenerationCodecRejectsUnknownAndNoncanonicalScope(t *testing.T) {
	bundle := callbackBundle("state", false, "none")
	token, err := parseTokenResponse(200, http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"access_token":"opaque","token_type":"Bearer","scope":"alpha zeta"}`), nil, flowTime)
	require.NoError(t, err)
	encoded, err := encodeTokenGeneration(bundle, token, flowTime)
	require.NoError(t, err)
	decoded, err := DecodeTokenGeneration(encoded)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zeta"}, decoded.Scopes)

	unknown := append([]byte(nil), encoded[:len(encoded)-1]...)
	unknown = append(unknown, []byte(`,"unknown":true}`)...)
	_, err = DecodeTokenGeneration(unknown)
	assert.ErrorIs(t, err, ErrTokenRejected)
}
