package configdefinition

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialConfigurationKeysExistOnlyInEnvelope(t *testing.T) {
	for kind, spec := range map[string]string{
		"http_credential": `{"name":"API","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"http_credential/api/secret"}`,
		"git_credential":  `{"name":"Git","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"git_credential/api/secret"}`,
	} {
		for _, key := range []string{`"other"`, `""`} {
			t.Run(kind+key, func(t *testing.T) {
				_, err := Normalize(definition(kind, "api", strings.Replace(spec, `{`, `{"key":`+key+`,`, 1)))
				require.ErrorIs(t, err, ErrInvalid, "a second spec key must not be silently overridden or ignored")
			})
		}
	}
}
