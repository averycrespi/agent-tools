package configdefinition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComparisonNormalizesHTTPPrivateDefaultWithoutSortingMethods(t *testing.T) {
	spec := `{"agent":{"kind":"agent","key":"builder"},"description":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"values":["POST","GET"]},"path":{"kind":"any"}}},"expires_at":null}`
	omitted := definition("http_grant", "access", spec)
	explicit := omitted
	explicit.Spec = json.RawMessage(strings.Replace(spec, `"type":"allow_requests"`, `"type":"allow_requests","allow_private":false`, 1))
	equal, err := Equivalent(omitted, explicit)
	require.NoError(t, err)
	require.True(t, equal)
	reordered := omitted
	reordered.Spec = json.RawMessage(strings.Replace(spec, `["POST","GET"]`, `["GET","POST"]`, 1))
	equal, err = Equivalent(omitted, reordered)
	require.NoError(t, err)
	require.False(t, equal)
}
