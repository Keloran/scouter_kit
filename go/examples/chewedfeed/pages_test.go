package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestOnlyRequestedChewedfeedDataIsDisplayed(t *testing.T) {
	data := snapshot{
		{source: source{key: "summary"}, rows: []record{{"projects": float64(2), "customers": float64(10), "paid_customers": float64(3), "score": float64(99), "deploys": "excluded-deploys"}}, at: time.Now()},
		{source: source{key: "health", title: "excluded-health"}, rows: []record{{"message": "excluded-health"}}},
		{source: source{key: "customers"}, rows: []record{{"name": "excluded-customer", "email": "excluded-email"}}},
		{source: source{key: "enquiries"}, rows: []record{{"message": "excluded-enquiry"}}},
	}
	pages := makePages(data, 44, 10, utf8.RuneCountInString)
	if len(pages) != 1 || pages[0].title != "Chewedfeed" {
		t.Fatalf("unexpected pages: %#v", pages)
	}
	if got := strings.Join(pages[0].lines, " / "); got != "Projects: 2 / Customers: 10 / Paid customers: 3" {
		t.Fatalf("summary fields: %s", got)
	}
	if preservePage(pages, "summary") != 0 {
		t.Fatal("lost overview identity")
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
				"description": strings.Repeat("Long description ", 100), "health_score": float64(80), "task_velocity_score": float64(42),
				"links": []any{map[string]any{"label": "Site", "url": strings.Repeat("long-url", 100)}}},
			{"id": "two", "slug": "two", "name": "Second site", "stage": "building"},
		}, at: time.Now()}
	data := snapshot{
		{source: source{key: "summary", title: "Chewedfeed", fields: []string{"projects"}}, rows: []record{{"projects": float64(2)}}},
		projects,
		{source: source{key: "one/uptime"}, rows: []record{{"overall_status": "up", "uptime_24h": float64(1), "uptime_30d": float64(.999)}}},
		{source: source{key: "one/tickets", list: true}, rows: []record{{"title": "Ticket A"}, {"title": "Ticket B"}}},
		{source: source{key: "customers", list: true}, rows: []record{
			{"product_slug": "one", "plan": "pro", "billing_status": "active"},
			{"product_slug": "one", "plan": "free", "billing_status": "active"},
			{"product_slug": "two", "plan": "pro", "billing_status": "canceled"},
		}},
		{source: source{key: "enquiries", list: true}, rows: []record{{"product_slug": "one", "message": strings.Repeat("message", 100)}}},
	}
	pages := makePages(data, 60, 8, utf8.RuneCountInString)
	if len(pages) != 3 {
		t.Fatalf("expected overview and exactly one page per site, got %d", len(pages))
	}
	first := pages[1]
	if first.title != "First site" {
		t.Fatalf("site name missing: %q", first.title)
	}
	for _, want := range []string{"PRs: 3", "Action: fix", "Stage: launched", "Overall status: up", "Health: 80/100", "Uptime: 24h 100.00% / 30d 99.90%", "Velocity: 42/100", "Customers: 2"} {
		if !strings.Contains(strings.Join(first.lines, "\n"), want) {
			t.Errorf("priority field missing: %q from %#v", want, first.lines)
		}
	}
	for _, p := range pages {
		if len(p.lines) > 8 {
			t.Fatalf("page overflow: %#v", p)
		}
		for _, line := range p.lines {
			if utf8.RuneCountInString(line) > 60 {
				t.Fatalf("line overflow: %q", line)
			}
		}
	}
	second := strings.Join(pages[2].lines, "\n")
	for _, want := range []string{"PRs: unknown", "Action: unknown", "Stage: building", "Overall status: unknown", "Health: unknown", "Uptime: unknown", "Velocity: unknown", "Customers: 1"} {
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

func TestUptimeFractionsAreDisplayedAsPercentages(t *testing.T) {
	for _, key := range []string{"uptime_24h", "uptime_30d"} {
		for _, tc := range []struct {
			fraction float64
			want     string
		}{{1, "100.00%"}, {0.999, "99.90%"}, {0.01, "1.00%"}, {0, "0.00%"}} {
			if got := fieldValue(key, tc.fraction); got != tc.want {
				t.Errorf("%s = %g: got %q, want %q", key, tc.fraction, got, tc.want)
			}
		}
	}
	if got := fieldValue("github_activity_score", float64(1)); got != "1" {
		t.Fatal("uptime scaling leaked into other metrics")
	}
}

func TestFooterStatusKeepsFetchTimeAndStaleWarning(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"Fetched 12:30", "Fetched 12:30"},
		{"STALE details / Fetched 12:30 / HTTP 503", "STALE Fetched 12:30 / HTTP 503"},
		{"STALE Cached 11:55", "STALE Cached 11:55"},
		{"STALE / cache unavailable", "STALE"},
	} {
		if got := footerStatus(tc.status); got != tc.want {
			t.Errorf("footerStatus(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
