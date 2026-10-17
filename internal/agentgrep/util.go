package agentgrep

import "strings"

func contains(s, sub string) bool   { return strings.Contains(s, sub) }
func trimLeftSpace(s string) string { return strings.TrimLeft(s, " \t\r\n") }
func trimSpace(s string) string     { return strings.TrimSpace(s) }
