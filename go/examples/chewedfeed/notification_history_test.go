package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNotificationHistoryKeepsLatestFiveNewestFirst(t *testing.T) {
	var h notificationHistory
	if pages := h.pages(40, 8, utf8.RuneCountInString); len(pages) != 1 || pages[0].id != "notifications/empty" {
		t.Fatal("empty history missing")
	}
	for i := 1; i <= 7; i++ {
		h.add(notification{App: "Test", Summary: fmt.Sprintf("Notification %d", i)}, time.Date(2026, 11, 6, 12, i, 0, 0, time.UTC))
	}
	pages := h.pages(40, 8, utf8.RuneCountInString)
	if len(h.entries) != 5 || len(pages) != 5 {
		t.Fatalf("history limit failed: %d entries, %d pages", len(h.entries), len(pages))
	}
	for i, p := range pages {
		if p.lines[0] != fmt.Sprintf("Notification %d", 7-i) {
			t.Fatal("notifications not newest first")
		}
		if p.title != "Test" || !strings.HasPrefix(p.status, "Received ") {
			t.Fatal("app or receipt time missing")
		}
	}
}

func TestNotificationHistoryPaginatesWithoutLosingTextOrReadingPosition(t *testing.T) {
	var h notificationHistory
	body := strings.Repeat("世界", 25)
	h.add(notification{App: "Test", Summary: "Summary", Body: body}, time.Now())
	pages := h.pages(8, 2, utf8.RuneCountInString)
	var all strings.Builder
	for _, p := range pages {
		if len(p.lines) > 2 {
			t.Fatal("history page exceeds row budget")
		}
		for _, line := range p.lines {
			if utf8.RuneCountInString(line) > 8 {
				t.Fatal("history line exceeds width")
			}
			all.WriteString(line)
		}
	}
	if all.String() != "Summary"+body {
		t.Fatal("history truncated notification content")
	}
	n := newNavigation([]page{{id: "site"}}, []page{{id: "ai"}}, []page{{id: "cpu"}}, false)
	n.replace(viewNotifications, pages)
	n.selected = viewNotifications
	n.move(1)
	id := n.current().pages[n.current().index].id
	h.add(notification{App: "New", Summary: "New message"}, time.Now())
	n.replace(viewNotifications, h.pages(8, 2, utf8.RuneCountInString))
	if n.selected != viewNotifications || n.current().pages[n.current().index].id != id {
		t.Fatal("new notification interrupted history reading position")
	}
	for i := 0; i < 5; i++ {
		h.add(notification{Summary: "Replacement"}, time.Now())
	}
	for _, p := range h.pages(8, 2, utf8.RuneCountInString) {
		if strings.HasPrefix(p.id, "notifications/1/") {
			t.Fatal("eviction retained pages from an old notification")
		}
	}
}
