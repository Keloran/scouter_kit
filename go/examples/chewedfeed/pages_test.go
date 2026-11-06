package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPaginationPreservesLongContentAndHidesInternalFields(t *testing.T) {
	message := strings.Repeat("longword", 30)
	data := snapshot{{source: source{key: "test", title: "Test", fields: []string{"message", "links"}},
		rows: []record{{"id": "entry", "message": message, "enquiry_key": "must-never-display",
			"links": []any{map[string]any{"label": "Site", "url": "https://example.com/a/long/path", "id": "internal"}}}},
		at: time.Now(), err: "HTTP 503"}}
	pages := makePages(data, 20, 3, utf8.RuneCountInString)
	var all strings.Builder
	for _, p := range pages {
		if len(p.lines) > 3 || !strings.HasPrefix(p.status, "STALE") {
			t.Fatal("page overflow or missing stale marker")
		}
		for _, line := range p.lines {
			if utf8.RuneCountInString(line) > 20 {
				t.Fatalf("line overflow: %q", line)
			}
			all.WriteString(line)
		}
	}
	if !strings.Contains(all.String(), message) || !strings.Contains(all.String(), "https://example.com/a/long/path") {
		t.Fatal("pagination lost long content")
	}
	if strings.Contains(all.String(), "must-never-display") || strings.Contains(all.String(), "internal") {
		t.Fatal("internal fields leaked")
	}
	if got := preservePage(pages, pages[1].id); got != 1 {
		t.Fatalf("lost reading position: %d", got)
	}
}

func TestWrapUnicodeAndControls(t *testing.T) {
	clean := value("hello\x00\x1b\nworld")
	if strings.ContainsAny(clean, "\x00\x1b\n") {
		t.Fatal("control characters survived")
	}
	input := "世界世界世界"
	lines := wrap(input, 3, utf8.RuneCountInString)
	if strings.Join(lines, "") != input || len(lines) != 2 {
		t.Fatalf("damaged Unicode: %#v", lines)
	}
}
