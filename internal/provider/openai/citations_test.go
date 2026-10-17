package openai

import (
	"encoding/json"
	"testing"
)

// citationAt builds a url_citation annotation over a marker occurrence.
func citationAt(text, marker, url, title string) map[string]any {
	start := 0
	for idx := range text {
		if idx+len(marker) <= len(text) && text[idx:idx+len(marker)] == marker {
			start = len([]rune(text[:idx]))
			break
		}
	}
	return map[string]any{
		"type": "url_citation", "start_index": start,
		"end_index": start + len([]rune(marker)), "url": url, "title": title,
	}
}

func renderAnnotations(t *testing.T, text string, annotations any) string {
	t.Helper()
	encoded, err := json.Marshal(annotations)
	if err != nil {
		t.Fatal(err)
	}
	return RenderAnnotatedText(text, encoded)
}

// Ported: native_marker_uses_real_annotation_even_after_multibyte_prose.
func TestNativeMarkerAfterMultibyteProse(t *testing.T) {
	marker := "\uE200cite\uE202turn0search12\uE201"
	text := "许可 🦀. " + marker + "\n\n"
	annotations := []any{citationAt(text, marker,
		"https://www.apple.com/legal/sla/docs/macOSTahoe.pdf", "macOS license")}
	got := renderAnnotations(t, text, annotations)
	want := "许可 🦀. [macOS license](<https://www.apple.com/legal/sla/docs/macOSTahoe.pdf>)\n\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Ported: shared_span_preserves_multiple_sources_and_deduplicates.
func TestSharedSpanMultipleSources(t *testing.T) {
	marker := "\uE200cite\uE202turn0search1\uE202turn0search2\uE201"
	a := citationAt(marker, marker, "https://a.example", "A")
	b := citationAt(marker, marker, "https://b.example", "B")
	got := renderAnnotations(t, marker, []any{a, b, a})
	want := "[A](<https://a.example>) [B](<https://b.example>)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Ported: ordinary_annotated_prose_is_preserved.
func TestOrdinaryProsePreserved(t *testing.T) {
	text := "A sourced claim. Next sentence."
	annotations := []any{citationAt(text, "A sourced claim.", "https://a.example", "Source")}
	got := renderAnnotations(t, text, annotations)
	want := "A sourced claim. [Source](<https://a.example>) Next sentence."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Ported: provider_rendered_markdown_citations_are_not_duplicated.
func TestProviderRenderedCitationsNotDuplicated(t *testing.T) {
	for _, source := range []string{
		"([rust-lang.org](https://rust-lang.org/tools/install/))",
		"[Install Rust](<https://rust-lang.org/tools/install/>)",
	} {
		text := "A sourced claim. " + source
		annotations := []any{citationAt(text, source,
			"https://rust-lang.org/tools/install/", "Install Rust")}
		if got := renderAnnotations(t, text, annotations); got != text {
			t.Errorf("source %q: got %q", source, got)
		}
	}
}

// Ported: missing_or_invalid_metadata_never_hides_or_invents_sources.
func TestInvalidMetadataNeverInventsSources(t *testing.T) {
	marker := "\uE200cite\uE202turn0search12\uE201"
	if got := RenderAnnotatedText(marker, nil); got != marker {
		t.Errorf("nil annotations: %q", got)
	}
	badCases := []any{
		map[string]any{"type": "url_citation", "start_index": 0, "end_index": 999, "url": "https://a.example"},
		map[string]any{"type": "url_citation", "start_index": 9, "end_index": 2, "url": "https://a.example"},
		citationAt(marker, marker, "javascript:alert(1)", "Unsafe"),
		citationAt(marker, marker, "file:///etc/passwd", "Unsafe"),
		citationAt(marker, marker, "https://a.example/\u001B]8", "Unsafe"),
		map[string]any{"type": "file_citation", "start_index": 0, "end_index": len([]rune(marker)), "url": "https://a.example"},
		map[string]any{"type": "url_citation", "start_index": 0, "end_index": len([]rune(marker))},
	}
	for i, bad := range badCases {
		if got := renderAnnotations(t, marker, []any{bad}); got != marker {
			t.Errorf("case %d: got %q want marker", i, got)
		}
	}
}
