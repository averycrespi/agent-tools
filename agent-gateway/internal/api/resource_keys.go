package api

import (
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func decodeCreateKey(raw json.RawMessage, key *string) bool {
	return raw == nil || decodeRequiredGrantMember(raw, key) && contract.ValidResourceKey(*key)
}
