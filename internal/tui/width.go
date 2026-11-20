// Terminal display width (wcwidth subset, no deps): CJK ideographs,
// kana, Hangul, fullwidth forms count as 2 columns; combining marks 0.
package tui

import "unicode"

// wideRanges lists the double-width script blocks (pragmatic subset:
// CJK ideographs via Han, kana, Hangul syllables).
var wideRanges = []*unicode.RangeTable{
	unicode.Han,
	unicode.Hangul,
	unicode.Hiragana,
	unicode.Katakana,
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case unicode.Is(unicode.Mn, r): // combining marks: zero width
		return 0
	case r >= 0x3000 && r <= 0x303F, // CJK punctuation
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x3400 && r <= 0x4DBF,   // CJK ext A
		r >= 0x20000 && r <= 0x2A6DF: // CJK ext B
		return 2
	}
	for _, table := range wideRanges {
		if unicode.Is(table, r) {
			return 2
		}
	}
	return 1
}

// textWidth sums the display widths of the runes in text (ANSI-aware when
// used via visibleLen).
func textWidth(text string) int {
	width := 0
	for _, r := range text {
		width += runeWidth(r)
	}
	return width
}
