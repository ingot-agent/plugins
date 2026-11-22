package contextcompact

import (
	"strings"
	"testing"
)

func TestParseSummaryIsForgiving(t *testing.T) {
	for name, test := range map[string]struct {
		answer      string
		wantSummary string
		wantNotes   string
	}{
		"canonical": {
			answer:      "SUMMARY:\nthe summary\n\nNOTES:\n- a\n- b",
			wantSummary: "the summary",
			wantNotes:   "- a\n- b",
		},
		"no headings": {
			answer:      "just prose about the work",
			wantSummary: "just prose about the work",
		},
		"notes only": {
			answer:      "NOTES:\n- a",
			wantNotes:   "- a",
			wantSummary: "",
		},
		"code fence": {
			answer:      "```markdown\nSUMMARY:\nfenced summary\n```",
			wantSummary: "fenced summary",
		},
		"lowercase headings": {
			answer:      "summary:\nlower\n\nnotes:\n- n",
			wantSummary: "lower",
			wantNotes:   "- n",
		},
		"preamble before summary": {
			answer:      "Sure, here it is:\nSUMMARY:\nafter preamble",
			wantSummary: "after preamble",
		},
		"heading inside a sentence is ignored": {
			answer:      "the log said SUMMARY: nothing\nand NOTES: nothing",
			wantSummary: "the log said SUMMARY: nothing\nand NOTES: nothing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			summary, notes := parseSummary(test.answer)
			if summary != test.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, test.wantSummary)
			}
			if notes != test.wantNotes {
				t.Fatalf("notes = %q, want %q", notes, test.wantNotes)
			}
		})
	}
}

func TestMergeNotesKeepsPreviousWhenMissing(t *testing.T) {
	if got := mergeNotes("- old", "", 100); got != "- old" {
		t.Fatalf("notes = %q", got)
	}
	if got := mergeNotes("- old", "- new", 100); got != "- new" {
		t.Fatalf("notes = %q", got)
	}
	long := strings.Repeat("x", 50)
	if got := mergeNotes("", long, 10); len(got) > 10 {
		t.Fatalf("notes not truncated: %d bytes", len(got))
	}
}

func TestTruncateUTF8KeepsRuneBoundaries(t *testing.T) {
	text := "中文中文中文"
	for size := 0; size <= len(text); size++ {
		got := truncateUTF8(text, size)
		if !strings.Contains(text, got) {
			t.Fatalf("size %d produced %q", size, got)
		}
	}
}
