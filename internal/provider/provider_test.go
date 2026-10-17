package provider

import (
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/provider/gemini"
	"github.com/ChaosJohn/gcode/internal/provider/openai"
	"github.com/ChaosJohn/gcode/internal/types"
)

// The Gemini adapter honors the static catalog and stores model switches.
func TestGeminiAdapterModelLifecycle(t *testing.T) {
	adapter := NewGemini("", GeminiOptions{})
	if adapter.Name() != "gemini" || adapter.Model() != gemini.DefaultModel {
		t.Fatalf("defaults: %s/%s", adapter.Name(), adapter.Model())
	}
	if err := adapter.SetModel("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if adapter.Model() != "gemini-2.5-pro" {
		t.Fatalf("model: %s", adapter.Model())
	}
	if err := adapter.SetModel("claude-x"); ErrUnknownModel == nil || err == nil {
		t.Fatalf("non-gemini model rejected: %v", err)
	}
	models := adapter.AvailableModels()
	if len(models) == 0 || models[0] != gemini.DefaultModel {
		t.Fatalf("catalog: %v", models)
	}
	if adapter.SupportsCompaction() {
		t.Fatalf("gemini has no native compaction")
	}
	if !adapter.SupportsImageInput() {
		t.Fatalf("gemini accepts images")
	}
}

// Fork shares the HTTP client and model; switches on the fork don't leak back.
func TestGeminiAdapterForkIsolation(t *testing.T) {
	adapter := NewGemini("gemini-2.5-pro", GeminiOptions{})
	forked := adapter.Fork()
	if err := forked.SetModel("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if adapter.Model() != "gemini-2.5-pro" || forked.Model() != "gemini-2.5-flash" {
		t.Fatalf("fork isolation: %s vs %s", adapter.Model(), forked.Model())
	}
}

// The OpenAI adapter surfaces the codex machine name.
func TestOpenAIAdapterShape(t *testing.T) {
	adapter := NewOpenAI("gpt-5.4", OpenAIOptions{
		Credentials: func() (*openai.CodexCredentials, error) { return nil, nil },
	})
	if adapter.Name() != "codex" {
		t.Fatalf("name: %s", adapter.Name())
	}
	if adapter.Model() != "gpt-5.4" {
		t.Fatalf("model: %s", adapter.Model())
	}
	if adapter.AvailableModels() != nil {
		t.Fatalf("catalog arrives after M4: %v", adapter.AvailableModels())
	}
}

// ValidateModel rejects catalog misses and accepts catalog hits.
func TestValidateModel(t *testing.T) {
	if err := ValidateModel([]string{"a", "b"}, "b"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateModel([]string{"a", "b"}, "c"); err == nil ||
		!strings.Contains(err.Error(), "not in the provider catalog") {
		t.Fatalf("catalog miss: %v", err)
	}
	if err := ValidateModel(nil, "anything"); err != nil {
		t.Fatalf("open catalog accepts any model: %v", err)
	}
}

// Dynamic system context wraps into an in-band user message.
func TestMessagesWithDynamicSystemContext(t *testing.T) {
	messages := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}}
	wrapped := MessagesWithDynamicSystemContext(messages, "today is tuesday")
	if len(wrapped) != 2 || wrapped[0].Role != types.RoleUser {
		t.Fatalf("prefix message")
	}
	if !strings.Contains(*wrapped[0].Content[0].Text, "<system_context>") ||
		!strings.Contains(*wrapped[0].Content[0].Text, "today is tuesday") {
		t.Fatalf("context text: %s", *wrapped[0].Content[0].Text)
	}
	kept := MessagesWithDynamicSystemContext(messages, "")
	if len(kept) != len(messages) {
		t.Fatalf("no dynamic system → transcript untouched")
	}
}
