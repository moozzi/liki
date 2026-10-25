package tui

import (
	"slices"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`list`, []string{"list"}},
		{`  add  https://x.y   -tags a,b `, []string{"add", "https://x.y", "-tags", "a,b"}},
		{`edit 3 -tags "go, maps"`, []string{"edit", "3", "-tags", "go, maps"}},
		{`edit 3 -tags ''`, []string{"edit", "3", "-tags", ""}},
		{`edit 3 -tags 'it\'s'`, nil}, // backslash is literal inside single quotes
		{`edit 3 -tags "say \"hi\""`, []string{"edit", "3", "-tags", `say "hi"`}},
		{`rm #3 #4`, []string{"rm", "#3", "#4"}},
		{`a\ b`, []string{"a b"}},
	}
	for _, tt := range tests {
		got, err := splitArgs(tt.in)
		if tt.want == nil {
			if err == nil {
				t.Errorf("splitArgs(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestSplitArgsUnterminated(t *testing.T) {
	if _, err := splitArgs(`rm "3`); err == nil {
		t.Error("want error for unterminated quote")
	}
}
