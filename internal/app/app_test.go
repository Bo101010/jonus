package app

import "testing"

// TestWithStyle pins the join between a description and its style tags.
//
// Both transports depend on it and neither can see the other, so a change here
// would silently make the CLI and the web UI produce different prompts.
func TestWithStyle(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		style  string
		want   string
	}{
		{name: "both", prompt: "轻快的民谣", style: "木吉他, 温暖", want: "轻快的民谣, 木吉他, 温暖"},
		{name: "prompt only", prompt: "轻快的民谣", want: "轻快的民谣"},
		{name: "style only", style: "lofi", want: "lofi"},
		{name: "neither", want: ""},
		{name: "trims both", prompt: "  a  ", style: "  b  ", want: "a, b"},
		{name: "blank style is not a separator", prompt: "a", style: "   ", want: "a"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := withStyle(test.prompt, test.style); got != test.want {
				t.Errorf("withStyle(%q, %q) = %q, want %q", test.prompt, test.style, got, test.want)
			}
		})
	}
}

func TestDeriveTitle(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "short", prompt: "海边", want: "海边"},
		{name: "collapses whitespace", prompt: "a\n\n  b\tc", want: "a b c"},
		{name: "empty", prompt: "   ", want: "Untitled"},
		{name: "truncates by runes", prompt: "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二", want: "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十…"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deriveTitle(test.prompt); got != test.want {
				t.Errorf("deriveTitle(%q) = %q, want %q", test.prompt, got, test.want)
			}
		})
	}
}
