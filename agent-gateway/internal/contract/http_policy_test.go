package contract

import (
	"reflect"
	"testing"
)

func TestHTTPV1Contract(t *testing.T) {
	if HTTPPolicyVersion != 1 || !reflect.DeepEqual(HTTPGrantTypes(), []HTTPGrantType{"block_destination", "allow_tunnel", "block_requests", "allow_requests"}) {
		t.Fatal("HTTP v1 dialect changed")
	}
	if HTTPDefaultBlock != "block" || HTTPDefaultAllow != "allow" || HTTPPathPrefix != "segment_prefix" {
		t.Fatal("HTTP v1 selectors/defaults changed")
	}
	if HTTPPolicyBytes != 16384 || HTTPPolicyDepth != 8 || HTTPPolicyGrants != 4096 || HTTPPolicyOrigins != 64 || HTTPPolicyCredentials != 256 || HTTPPolicyMethods != 32 || HTTPMethodBytes != 32 || HTTPHostBytes != 253 || HTTPPathBytes != 4096 || HTTPTargetBytes != 8192 || HTTPAddressFacts != 64 {
		t.Fatal("HTTP v1 bounds changed")
	}
}
