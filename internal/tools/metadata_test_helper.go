package tools

import (
	"encoding/json"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// MetadataDiff extracts the stored diff from a tool output's metadata (test
// helper; nil-safe).
func MetadataDiff(output toolcore.ToolOutput) string { return metadataDiff(output) }

func metadataDiff(output toolcore.ToolOutput) string {
	var meta struct {
		Diff string `json:"diff"`
	}
	if len(output.Metadata) == 0 {
		return ""
	}
	_ = json.Unmarshal(output.Metadata, &meta)
	return meta.Diff
}
