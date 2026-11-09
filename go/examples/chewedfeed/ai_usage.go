package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage comes from the existing local ai-usagebar cache; no credentials or conversations are read.
type usageWindow struct {
	name  string
	used  *float64
	reset time.Time
}

func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d.Minutes())
	if m >= 24*60 {
		return fmt.Sprintf("%dd %dh", m/(24*60), m/60%24)
	}
	if m >= 60 {
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

func openAIUsage(data []byte) ([]usageWindow, []string, error) {
	type window struct {
		Used    *float64 `json:"used_percent"`
		Seconds int64    `json:"limit_window_seconds"`
		Reset   int64    `json:"reset_at"`
	}
	var record struct {
		Plan   string `json:"plan_type"`
		Limits struct {
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, nil, err
	}
	var windows []usageWindow
	for i, w := range []*window{record.Limits.Primary, record.Limits.Secondary} {
		if w == nil {
			continue
		}
		name := "Session"
		if i == 1 {
			name = "Weekly"
		}
		if w.Seconds > 0 && w.Seconds != 7*24*60*60 {
			name = strings.ReplaceAll(shortDuration(time.Duration(w.Seconds)*time.Second), " 0m", "")
		}
		var reset time.Time
		if w.Reset > 0 {
			reset = time.Unix(w.Reset, 0)
		}
		windows = append(windows, usageWindow{name, w.Used, reset})
	}
	var extra []string
	if record.Plan != "" {
		extra = append(extra, "Plan: "+value(record.Plan))
	}
	return windows, extra, nil
}

func claudeUsage(data []byte) ([]usageWindow, []string, error) {
	type window struct {
		Used  *float64 `json:"utilization"`
		Reset string   `json:"resets_at"`
	}
	var record struct {
		Session *window `json:"five_hour"`
		Weekly  *window `json:"seven_day"`
		Opus    *window `json:"seven_day_opus"`
		Sonnet  *window `json:"seven_day_sonnet"`
		Extra   struct {
			Enabled bool `json:"is_enabled"`
		} `json:"extra_usage"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, nil, err
	}
	var windows []usageWindow
	for i, w := range []*window{record.Session, record.Weekly, record.Opus, record.Sonnet} {
		if w != nil {
			reset, _ := time.Parse(time.RFC3339Nano, w.Reset)
			windows = append(windows, usageWindow{[]string{"5h", "Weekly", "Opus week", "Sonnet week"}[i], w.Used, reset})
		}
	}
	var extra []string
	if record.Extra.Enabled {
		extra = append(extra, "Extra usage: enabled")
	}
	return windows, extra, nil
}

func readUsageCache(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(data) > limit {
		return nil, time.Time{}, errors.New("usage cache is unreadable or too large")
	}
	return data, info.ModTime(), nil
}

func aiUsagePages(dir string, previous []page, now time.Time) []page {
	providers := []struct {
		id, title, directory string
		decode               func([]byte) ([]usageWindow, []string, error)
	}{
		{"ai/codex", "Codex usage", "openai", openAIUsage},
		{"ai/claude", "Claude Code usage", "anthropic", claudeUsage},
	}
	var pages []page
	for _, provider := range providers {
		p := page{id: provider.id, title: provider.title, status: "Unavailable"}
		data, updated, err := readUsageCache(filepath.Join(dir, provider.directory, "usage.json"))
		var windows []usageWindow
		var extra []string
		if err == nil {
			windows, extra, err = provider.decode(data)
		}
		if err != nil || dir == "" {
			p.lines = []string{"No readable local usage cache", "Run ai-usagebar to refresh usage"}
			for _, old := range previous {
				if old.id == p.id && old.status != "Unavailable" {
					p.lines, p.status = old.lines, "STALE / cache unavailable"
				}
			}
			pages = append(pages, p)
			continue
		}
		p.status = "Cached " + updated.Local().Format("15:04")
		if now.Sub(updated) > 5*time.Minute {
			p.status = "STALE " + p.status
		}
		for _, w := range windows {
			if !w.reset.IsZero() && !now.Before(w.reset) && updated.Before(w.reset) {
				p.lines = append(p.lines, w.name+": awaiting cache refresh")
				if !strings.HasPrefix(p.status, "STALE") {
					p.status = "STALE " + p.status
				}
				continue
			}
			if w.used == nil || math.IsNaN(*w.used) || math.IsInf(*w.used, 0) || *w.used < 0 {
				p.lines = append(p.lines, w.name+": usage unknown")
			} else {
				p.lines = append(p.lines, fmt.Sprintf("%s: %.1f%% used (%.1f%% left)", w.name, *w.used, math.Max(0, 100-*w.used)))
			}
			reset := "Reset: unknown"
			if !w.reset.IsZero() {
				reset = "Reset: " + w.reset.Local().Format("02 Jan 15:04")
				if now.Before(w.reset) {
					reset += " (" + shortDuration(w.reset.Sub(now)) + ")"
				}
			}
			p.lines = append(p.lines, reset)
		}
		if len(windows) == 0 {
			p.lines = append(p.lines, "No usage windows reported")
		}
		p.lines = append(p.lines, extra...)
		pages = append(pages, p)
	}
	return pages
}
