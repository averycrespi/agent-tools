package output

import (
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
)

// Format converts a ToolResult's content blocks into a JSON array.
// Each text block is parsed as JSON if valid; otherwise included as a string.
// Non-text blocks are ignored.
func Format(result *client.ToolResult) (string, error) {
	items := make([]any, 0, len(result.Content))
	for _, block := range result.Content {
		if block.Type != "text" {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(block.Text), &parsed); err == nil {
			items = append(items, parsed)
		} else {
			items = append(items, block.Text)
		}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("marshal output: %w", err)
	}
	return string(data), nil
}
