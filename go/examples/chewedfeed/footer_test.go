package main

import "testing"

func TestFooterSourceIconsKeepTimesAndStaleState(t *testing.T) {
	for _, tc := range []struct {
		status, icon, text string
		stale              bool
	}{
		{"Fetched 12:30", "download", "12:30", false},
		{"Cached 11:55", "cache", "11:55", false},
		{"Sampled 12:30:05", "sample", "12:30:05", false},
		{"STALE details / Fetched 12:30", "download", "12:30", true},
		{"STALE / cache unavailable", "unknown", "--", true},
		{"Unavailable", "unknown", "--", false},
	} {
		got := sourceIndicator(tc.status)
		if got.icon != tc.icon || got.text != tc.text || got.stale != tc.stale {
			t.Fatalf("%q: %+v", tc.status, got)
		}
	}
	for name, rows := range statusIcons {
		if len(rows) == 0 || len(rows) > 11 {
			t.Fatalf("invalid icon height: %s", name)
		}
		for _, row := range rows {
			if len(row) != len(rows[0]) {
				t.Fatalf("uneven icon rows: %s", name)
			}
		}
	}
}
