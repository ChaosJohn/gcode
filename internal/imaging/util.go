package imaging

import "strings"

func equalFold(a, b string) bool           { return strings.EqualFold(a, b) }
func hasPrefix(s, prefix string) bool      { return strings.HasPrefix(s, prefix) }
func contains(s, sub string) bool          { return strings.Contains(s, sub) }
func stringsTrimBOM(s string) string       { return strings.TrimPrefix(s, "\ufeff") }
func stringsTrimLeftSpace(s string) string { return strings.TrimLeft(s, " \t\r\n") }
