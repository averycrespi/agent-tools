package contract

import "encoding/json"

const SyntheticCatalogRevision = "1"

type SyntheticSelfServiceTool struct {
	ID              string
	ServerID        string
	UpstreamName    string
	ExternalName    string
	CatalogRevision string
	Descriptor      NormalizedToolDescriptor
	Canonical       json.RawMessage
	Fingerprint     string
}

var syntheticSelfServiceTools = []SyntheticSelfServiceTool{
	newSyntheticTool(
		"00000000000000000000000001", "get_identity", "Get identity", "Return the authenticated principal's identity.",
		json.RawMessage(`{"type":"object","additionalProperties":false}`), true, false, true,
		json.RawMessage(`{"annotations":{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true,"title":"Get identity"},"description":"Return the authenticated principal's identity.","inputSchema":{"additionalProperties":false,"type":"object"},"name":"get_identity","title":"Get identity"}`),
		"cc982af50fbc4873c57e89b5052a3c725f5e3898b2142dab096b99b0a4e656b9",
	),
	newSyntheticTool(
		"00000000000000000000000002", "list_grants", "List grants", "List grants belonging to the authenticated principal.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["cursor"],"properties":{"cursor":{"type":["string","null"],"maxLength":512}}}`), true, false, true,
		json.RawMessage(`{"annotations":{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true,"title":"List grants"},"description":"List grants belonging to the authenticated principal.","inputSchema":{"additionalProperties":false,"properties":{"cursor":{"maxLength":512,"type":["string","null"]}},"required":["cursor"],"type":"object"},"name":"list_grants","title":"List grants"}`),
		"6c400b6b857af7b7f6b58dc8254c8e9d3ea1d064ba1168e01c049aae3f0ea1f4",
	),
	newSyntheticTool(
		"00000000000000000000000003", "create_grant_request", "Create grant request", "Create or return an identical pending access request for the authenticated principal.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["policy"],"properties":{"policy":{"type":"object","additionalProperties":false,"required":["scope","target","constraint","duration_seconds","future_tools_acknowledged"],"properties":{"scope":{"enum":["tool","server"]},"target":{"type":"string","minLength":1,"maxLength":128},"constraint":{"anyOf":[{"type":"null"},{"type":"object","additionalProperties":false,"required":["equals"],"properties":{"equals":{"type":"object","minProperties":1,"maxProperties":16,"propertyNames":{"type":"string","minLength":1,"maxLength":256},"additionalProperties":{"type":["string","boolean","number","null"]}}}}]},"duration_seconds":{"anyOf":[{"type":"null"},{"type":"string","pattern":"^(?:[6-9][0-9]|[1-9][0-9]{2,5}|[12][0-9]{6})$"}]},"future_tools_acknowledged":{"type":"boolean"}}}}}`), false, false, false,
		json.RawMessage(`{"annotations":{"destructiveHint":false,"idempotentHint":false,"openWorldHint":false,"readOnlyHint":false,"title":"Create grant request"},"description":"Create or return an identical pending access request for the authenticated principal.","inputSchema":{"additionalProperties":false,"properties":{"policy":{"additionalProperties":false,"properties":{"constraint":{"anyOf":[{"type":"null"},{"additionalProperties":false,"properties":{"equals":{"additionalProperties":{"type":["string","boolean","number","null"]},"maxProperties":16,"minProperties":1,"propertyNames":{"maxLength":256,"minLength":1,"type":"string"},"type":"object"}},"required":["equals"],"type":"object"}]},"duration_seconds":{"anyOf":[{"type":"null"},{"pattern":"^(?:[6-9][0-9]|[1-9][0-9]{2,5}|[12][0-9]{6})$","type":"string"}]},"future_tools_acknowledged":{"type":"boolean"},"scope":{"enum":["tool","server"]},"target":{"maxLength":128,"minLength":1,"type":"string"}},"required":["scope","target","constraint","duration_seconds","future_tools_acknowledged"],"type":"object"}},"required":["policy"],"type":"object"},"name":"create_grant_request","title":"Create grant request"}`),
		"eb14cf705b755301f7f6aab0bcb5bb396d296ce21686521425011d2afe0bb6e4",
	),
	newSyntheticTool(
		"00000000000000000000000004", "get_grant_request", "Get grant request", "Return one grant request belonging to the authenticated principal.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":"^[0-9A-HJKMNP-TV-Z]{26}$"}}}`), true, false, true,
		json.RawMessage(`{"annotations":{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true,"title":"Get grant request"},"description":"Return one grant request belonging to the authenticated principal.","inputSchema":{"additionalProperties":false,"properties":{"id":{"pattern":"^[0-9A-HJKMNP-TV-Z]{26}$","type":"string"}},"required":["id"],"type":"object"},"name":"get_grant_request","title":"Get grant request"}`),
		"5906ef2ea8364faabc43416cc6953ae3c5438f74615f028fbb87290d58387b5d",
	),
	newSyntheticTool(
		"00000000000000000000000005", "list_grant_requests", "List grant requests", "List grant requests belonging to the authenticated principal.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["cursor","state"],"properties":{"cursor":{"type":["string","null"],"maxLength":512},"state":{"enum":[null,"pending","approved","rejected","cancelled"]}}}`), true, false, true,
		json.RawMessage(`{"annotations":{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true,"title":"List grant requests"},"description":"List grant requests belonging to the authenticated principal.","inputSchema":{"additionalProperties":false,"properties":{"cursor":{"maxLength":512,"type":["string","null"]},"state":{"enum":[null,"pending","approved","rejected","cancelled"]}},"required":["cursor","state"],"type":"object"},"name":"list_grant_requests","title":"List grant requests"}`),
		"3189c7d50d7bc6709d13a1c25ec21a1c1128acf362ec0567b9f315b4cc1666fa",
	),
	newSyntheticTool(
		"00000000000000000000000006", "cancel_grant_request", "Cancel grant request", "Cancel one pending grant request belonging to the authenticated principal.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":"^[0-9A-HJKMNP-TV-Z]{26}$"}}}`), false, true, true,
		json.RawMessage(`{"annotations":{"destructiveHint":true,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":false,"title":"Cancel grant request"},"description":"Cancel one pending grant request belonging to the authenticated principal.","inputSchema":{"additionalProperties":false,"properties":{"id":{"pattern":"^[0-9A-HJKMNP-TV-Z]{26}$","type":"string"}},"required":["id"],"type":"object"},"name":"cancel_grant_request","title":"Cancel grant request"}`),
		"f6414fe9dff78271e7a6851a70252b2b94e49c7b4399d145a55efbc51654e73f",
	),
}

func newSyntheticTool(id, name, title, description string, input json.RawMessage, readOnly, destructive, idempotent bool, canonical json.RawMessage, fingerprint string) SyntheticSelfServiceTool {
	return SyntheticSelfServiceTool{
		ID: id, ServerID: SyntheticServerID, UpstreamName: name, ExternalName: SyntheticServerNamespace + "." + name, CatalogRevision: SyntheticCatalogRevision,
		Descriptor: NormalizedToolDescriptor{
			Name: name, Title: stringAddress(title), Description: stringAddress(description), InputSchema: input,
			Annotations: NormalizedToolAnnotations{Title: stringAddress(title), ReadOnlyHint: readOnly, DestructiveHint: destructive, IdempotentHint: idempotent, OpenWorldHint: false},
		},
		Canonical: canonical, Fingerprint: fingerprint,
	}
}

func SyntheticSelfServiceTools() []SyntheticSelfServiceTool {
	result := make([]SyntheticSelfServiceTool, len(syntheticSelfServiceTools))
	for index, tool := range syntheticSelfServiceTools {
		result[index] = tool
		result[index].Descriptor.Title = copyString(tool.Descriptor.Title)
		result[index].Descriptor.Description = copyString(tool.Descriptor.Description)
		result[index].Descriptor.InputSchema = append(json.RawMessage(nil), tool.Descriptor.InputSchema...)
		result[index].Descriptor.OutputSchema = append(json.RawMessage(nil), tool.Descriptor.OutputSchema...)
		result[index].Descriptor.Annotations.Title = copyString(tool.Descriptor.Annotations.Title)
		result[index].Canonical = append(json.RawMessage(nil), tool.Canonical...)
	}
	return result
}

func stringAddress(value string) *string { return &value }

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringAddress(*value)
}
