package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeUsage(t *testing.T, dir, provider, data string, updated time.Time) {
	t.Helper()
	path := filepath.Join(dir, provider, "usage.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAIUsagePercentagesAndCacheRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	openai := fmt.Sprintf(`{"plan_type":"plus","private_metadata":"private-marker","rate_limit":{
"primary_window":{"used_percent":20.5,"limit_window_seconds":18000,"reset_at":%d},
"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":%d}}}`,
		now.Add(time.Hour).Unix(), now.Add(6*24*time.Hour).Unix())
	claude := fmt.Sprintf(`{"five_hour":{"utilization":60,"resets_at":%q},
"seven_day":{"utilization":0,"resets_at":%q},"extra_usage":{"is_enabled":true}}`,
		now.Add(time.Hour).Format(time.RFC3339), now.Add(6*24*time.Hour).Format(time.RFC3339))
	writeUsage(t, dir, "openai", openai, now.Add(-time.Minute))
	writeUsage(t, dir, "anthropic", claude, now.Add(-time.Minute))
	pages := aiUsagePages(dir, nil, now)
	if len(pages) != 2 || pages[0].title != "Codex usage" || pages[1].title != "Claude Code usage" {
		t.Fatalf("provider pages missing: %#v", pages)
	}
	for i, wants := range [][]string{
		{"5h: 20.5% used (79.5% left)", "Weekly: 0.0% used (100.0% left)", "Plan: plus"},
		{"5h: 60.0% used (40.0% left)", "Weekly: 0.0% used (100.0% left)", "Extra usage: enabled"},
	} {
		text := strings.Join(pages[i].lines, "\n")
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("missing usage information %q from %q", want, text)
			}
		}
		if strings.Contains(text, "private-marker") || strings.HasPrefix(pages[i].status, "STALE") {
			t.Fatal("private metadata leaked or fresh cache marked stale")
		}
	}
	writeUsage(t, dir, "openai", `{`, now)
	stale := aiUsagePages(dir, pages, now)
	if !strings.HasPrefix(stale[0].status, "STALE") || strings.Join(stale[0].lines, "\n") != strings.Join(pages[0].lines, "\n") {
		t.Fatal("failed cache read lost previous usage or hid its staleness")
	}
	writeUsage(t, dir, "openai", openai, now)
	recovered := aiUsagePages(dir, stale, now)
	if strings.HasPrefix(recovered[0].status, "STALE") {
		t.Fatal("valid cache did not recover")
	}
}

func TestAIUsageMissingAndExpiredWindows(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	pages := aiUsagePages(dir, nil, now)
	for _, p := range pages {
		if p.status != "Unavailable" || strings.Contains(strings.Join(p.lines, "\n"), "% left") {
			t.Fatal("missing cache was presented as available quota")
		}
	}
	data := fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":90,"reset_at":%d}}}`, now.Add(-time.Minute).Unix())
	writeUsage(t, dir, "openai", data, now.Add(-time.Hour))
	pages = aiUsagePages(dir, nil, now)
	if !strings.HasPrefix(pages[0].status, "STALE") || !strings.Contains(strings.Join(pages[0].lines, "\n"), "awaiting cache refresh") {
		t.Fatal("expired quota window was presented as current")
	}
	if strings.Contains(strings.Join(pages[0].lines, "\n"), "% left") {
		t.Fatal("remaining quota was fabricated for an expired cache")
	}
}

func TestAIUsageUnknownIsDifferentFromZero(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeUsage(t, dir, "openai", `{"rate_limit":{"primary_window":{},"secondary_window":{"used_percent":0}}}`, now)
	pages := aiUsagePages(dir, nil, now)
	text := strings.Join(pages[0].lines, "\n")
	if !strings.Contains(text, "Session: usage unknown") || !strings.Contains(text, "Weekly: 0.0% used (100.0% left)") {
		t.Fatal("missing usage and zero usage were conflated: " + text)
	}
}
