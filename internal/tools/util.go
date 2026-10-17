package tools

import "strings"

func contains(s, sub string) bool        { return strings.Contains(s, sub) }
func trimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }
func trimSpace(s string) string          { return strings.TrimSpace(s) }
