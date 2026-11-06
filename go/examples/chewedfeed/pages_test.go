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

func TestSitesStayOnOnePageWithPriorityFields(t *testing.T) {
	projects := result{source: source{key: "projects", title: "Projects", fields: projectFields},
		rows: []record{
			{"id": "one", "slug": "one", "name": "First site", "stage": "launched",
				"linear_progress_score": float64(0), "github_activity_score": float64(75),
				"github_open_prs": float64(3), "attention_mode": "fix", "last_synced_at": "2026-11-06T10:00:00Z",
				"description": strings.Repeat("Long description ", 100), "health_score": float64(80),
				"links": []any{map[string]any{"label": "Site", "url": strings.Repeat("long-url", 100)}}},
			{"id": "two", "slug": "two", "name": "Second site", "stage": "building"},
		}, at: time.Now()}
	data := snapshot{
		{source: source{key: "summary", title: "Chewedfeed", fields: []string{"projects"}}, rows: []record{{"projects": float64(2)}}},
		projects,
		{source: source{key: "one/uptime"}, rows: []record{{"overall_status": "up", "uptime_24h": float64(100)}}},
		{source: source{key: "one/tickets", list: true}, rows: []record{{"title": "Ticket A"}, {"title": "Ticket B"}}},
		{source: source{key: "customers", list: true}, rows: []record{
			{"product_slug": "one", "plan": "pro", "billing_status": "active"},
			{"product_slug": "one", "plan": "free", "billing_status": "active"},
			{"product_slug": "two", "plan": "pro", "billing_status": "canceled"},
		}},
		{source: source{key: "enquiries", list: true}, rows: []record{{"product_slug": "one", "message": strings.Repeat("message", 100)}}},
	}
	pages := makePages(data, 44, 6, utf8.RuneCountInString)
	if len(pages) != 3 {
		t.Fatalf("expected overview and exactly one page per site, got %d", len(pages))
	}
	first := pages[1]
	if first.title != "First site" {
		t.Fatalf("site name missing: %q", first.title)
	}
	for _, want := range []string{"Progress: 0%", "GitHub: 75/100  PRs: 3", "Action: fix", "Last sync:", "Stage: launched", "Customers: 2 (1 paid)"} {
		if !strings.Contains(strings.Join(first.lines, "\n"), want) {
			t.Errorf("priority field missing: %q from %#v", want, first.lines)
		}
	}
	for _, p := range pages {
		if len(p.lines) > 6 {
			t.Fatalf("page overflow: %#v", p)
		}
		for _, line := range p.lines {
			if utf8.RuneCountInString(line) > 44 {
				t.Fatalf("line overflow: %q", line)
			}
		}
	}
	second := strings.Join(pages[2].lines, "\n")
	for _, want := range []string{"Progress: unknown", "GitHub: unknown", "Action: unknown", "Last sync: never", "Stage: building", "Customers: 1 (0 paid)"} {
		if !strings.Contains(second, want) {
			t.Errorf("missing-data handling or customer grouping failed: %q", second)
		}
	}
}

func TestSiteDetailFailuresRemainVisibleWhenContentIsFull(t *testing.T) {
	data := snapshot{
		{source: source{key: "projects"}, rows: []record{{"slug": "one", "name": "Site"}}, at: time.Now()},
		{source: source{key: "one/uptime"}, err: "HTTP 503"},
	}
	pages := makePages(data, 44, 6, utf8.RuneCountInString)
	if len(pages) != 1 || !strings.HasPrefix(pages[0].status, "STALE") {
		t.Fatalf("detail error hidden or added extra pages: %#v", pages)
	}
}
