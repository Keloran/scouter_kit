package main

import (
	"fmt"
	"strings"
	"time"
)

const notificationHistoryLimit = 5

type receivedNotification struct {
	id   uint64
	note notification
	at   time.Time
}

// Kept only in memory; notification contents are never written to logs or files.
type notificationHistory struct {
	sequence uint64
	entries  []receivedNotification
}

func (h *notificationHistory) add(note notification, at time.Time) {
	h.sequence++
	h.entries = append([]receivedNotification{{id: h.sequence, note: note, at: at}}, h.entries...)
	h.entries = h.entries[:min(notificationHistoryLimit, len(h.entries))]
}

func emptyNotificationHistory() []page {
	return []page{{id: "notifications/empty", title: "Notifications", lines: []string{"No notifications yet"}, status: "History empty"}}
}

func (h notificationHistory) pages(width, rows int, measure func(string) int) []page {
	if len(h.entries) == 0 {
		return emptyNotificationHistory()
	}
	rows = max(1, rows)
	var pages []page
	for _, entry := range h.entries {
		title := entry.note.App
		if title == "" {
			title = "Notification"
		}
		text := entry.note.Summary
		if entry.note.Body != "" {
			if text != "" {
				text += "\n"
			}
			text += entry.note.Body
		}
		var lines []string
		for _, line := range strings.Split(text, "\n") {
			lines = append(lines, wrap(line, width, measure)...)
		}
		for start := 0; start < len(lines); start += rows {
			pages = append(pages, page{
				id: fmt.Sprintf("notifications/%d/%d", entry.id, start/rows), title: title,
				lines: lines[start:min(start+rows, len(lines))], status: "Received " + entry.at.Format("02 Jan 15:04"),
			})
		}
	}
	return pages
}
