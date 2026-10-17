package gemini

import (
	"errors"
	"os"
	"strings"
)

func errString(s string) error        { return errors.New(s) }
func strPtr(s string) *string         { return &s }
func trimSpace(s string) string       { return strings.TrimSpace(s) }
func asciiLower(s string) string      { return strings.ToLower(s) }
func contains(s, sub string) bool     { return strings.Contains(s, sub) }
func hasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }
func asciiEqualFold(a, b string) bool { return strings.EqualFold(a, b) }
func osGetenv(key string) string      { return os.Getenv(key) }
