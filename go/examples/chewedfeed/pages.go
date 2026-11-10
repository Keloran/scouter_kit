package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

type page struct {
	id, title string
	lines     []string
	status    string
}

// Explicit field lists keep internal IDs and credentials off the display.
func value(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return strings.Join(strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	case float64:
		return fmt.Sprintf("%.2f", v)
	case bool:
		if v {
			return "yes"
		}
		return "no"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func fieldValue(key string, v any) string {
	if n, ok := v.(float64); ok {
		if key == "uptime_24h" || key == "uptime_30d" {
			return fmt.Sprintf("%.2f%%", n*100)
		}
		if n == float64(int64(n)) {
			return fmt.Sprintf("%.0f", n)
		}
		return fmt.Sprintf("%.1f", n)
	}
	if s, ok := v.(string); ok && strings.HasSuffix(key, "_at") {
		if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return at.Local().Format("02 Jan 15:04")
		}
	}
	return value(v)
}

// wrap also splits long URLs and words. measure is the actual font advance on
// glasses, or a rune count for the terminal preview.
func wrap(s string, width int, measure func(string) int) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	for s != "" {
		r := []rune(s)
		n, space := 0, 0
		for n < len(r) && measure(string(r[:n+1])) <= width {
			n++
			if r[n-1] == ' ' {
				space = n
			}
		}
		if n == len(r) {
			lines = append(lines, strings.TrimSpace(s))
			break
		}
		if space > 0 {
			n = space
		}
		n = max(1, n)
		lines = append(lines, strings.TrimSpace(string(r[:n])))
		s = strings.TrimSpace(string(r[n:]))
	}
	return lines
}

// fitLine keeps a field on one row, indicating omitted text without splitting UTF-8.
func fitLine(s string, width int, measure func(string) int) string {
	if measure(s) <= width {
		return s
	}
	dots := "..."
	for dots != "" && measure(dots) > width {
		dots = dots[:len(dots)-1]
	}
	runes := []rune(s)
	for len(runes) > 0 && measure(strings.TrimSpace(string(runes))+dots) > width {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimSpace(string(runes)) + dots
}

func sourceStatus(r result) string {
	status := "Fetched " + r.at.Local().Format("15:04")
	if r.at.IsZero() {
		status = "Unavailable"
	}
	if r.err != "" {
		status = "STALE " + status
	}
	return status
}

func sitePage(row record, id, status string, data snapshot, width, rows int, measure func(string) int) page {
	title := value(row["name"])
	if title == "" {
		title = value(row["slug"])
	}
	if title == "" {
		title = "Unnamed project"
	}
	field := func(row record, key string) string {
		if v := fieldValue(key, row[key]); v != "" {
			return v
		}
		return "unknown"
	}
	score := func(key string) string {
		if row[key] == nil {
			return "unknown"
		}
		return field(row, key) + "/100"
	}
	slug := value(row["slug"])
	var uptime, tickets record
	var customerCount *int
	for _, r := range data {
		relevant := r.key == slug+"/uptime" || r.key == slug+"/ticket-counts" || r.key == "customers"
		if slug == "" || !relevant {
			continue
		}
		if r.err != "" && !strings.HasPrefix(status, "STALE") {
			status = "STALE " + status
		}
		if r.at.IsZero() && r.err != "" {
			continue
		}
		if r.key == slug+"/uptime" && len(r.rows) > 0 {
			uptime = r.rows[0]
		}
		if r.key == slug+"/ticket-counts" && len(r.rows) > 0 {
			tickets = r.rows[0]
		}
		if r.key == "customers" {
			count := 0
			for _, customer := range r.rows {
				if customer["product_slug"] == slug {
					count++
				}
			}
			customerCount = &count
		}
	}
	uptimeText := "unknown"
	if uptime["uptime_24h"] != nil || uptime["uptime_30d"] != nil {
		uptimeText = "24h " + field(uptime, "uptime_24h") + " / 30d " + field(uptime, "uptime_30d")
	}
	lines := []string{
		"PRs: " + field(row, "github_open_prs"),
		"Action: " + field(row, "attention_mode"),
		"Stage: " + field(row, "stage"),
		"Overall status: " + field(uptime, "overall_status"),
		"Health: " + score("health_score"),
		"Uptime: " + uptimeText,
		"Velocity: " + score("task_velocity_score"),
	}
	if customerCount != nil {
		lines = append(lines, fmt.Sprintf("Customers: %d", *customerCount))
	}
	// Whole columns per row, so "In progress 0" never splits.
	for i, part := range ticketCounts(tickets) {
		if last := len(lines) - 1; i > 0 && measure(lines[last]+" | "+part) <= width {
			lines[last] += " | " + part
		} else {
			lines = append(lines, part)
		}
	}
	lines = lines[:min(max(1, rows), len(lines))]
	for i := range lines {
		lines[i] = fitLine(lines[i], width, measure)
	}
	return page{"projects/" + id + "/0", title, lines, status}
}

// ticketCounts formats {"backlog": 2, "columns": [{"name": "To do", "count": 1}, ...]}
// as ["Backlog 2", "To do 1", ...]; it is empty when the project has no TrackLines board.
func ticketCounts(row record) []string {
	count := func(v any) string {
		if n, ok := v.(float64); ok {
			return fmt.Sprintf("%.0f", n)
		}
		return "?"
	}
	columns, _ := row["columns"].([]any)
	if row["backlog"] == nil && len(columns) == 0 {
		return nil
	}
	parts := []string{"Backlog " + count(row["backlog"])}
	for _, c := range columns {
		if c, ok := c.(map[string]any); ok {
			parts = append(parts, value(c["name"])+" "+count(c["count"]))
		}
	}
	return parts
}

func makePages(data snapshot, width, rows int, measure func(string) int) []page {
	var pages []page
	for _, r := range data {
		switch r.key {
		case "summary":
			var row record
			if len(r.rows) > 0 {
				row = r.rows[0]
			}
			lines := make([]string, 0, 3)
			for _, item := range []struct{ key, label string }{
				{"projects", "Projects"}, {"customers", "Customers"}, {"paid_customers", "Paid customers"},
			} {
				text := fieldValue(item.key, row[item.key])
				if text == "" {
					text = "unknown"
				}
				lines = append(lines, fitLine(item.label+": "+text, width, measure))
			}
			pages = append(pages, page{id: "summary", title: "Chewedfeed", lines: lines[:min(max(1, rows), len(lines))], status: sourceStatus(r)})
		case "projects":
			if len(r.rows) == 0 {
				text := "No projects"
				if r.err != "" {
					text = "Projects unavailable"
				}
				pages = append(pages, page{id: "projects", title: "Projects", lines: []string{text}, status: sourceStatus(r)})
			}
			for i, row := range r.rows {
				id := value(row["id"])
				if id == "" {
					id = value(row["slug"])
				}
				if id == "" {
					id = fmt.Sprint(i)
				}
				pages = append(pages, sitePage(row, id, sourceStatus(r), data, width, rows, measure))
			}
		}
	}
	if len(pages) == 0 {
		return []page{{id: "summary", title: "Chewedfeed", lines: []string{"No data available"}, status: "Unavailable"}}
	}
	return pages
}

func preservePage(pages []page, id string) int {
	for i, p := range pages {
		if p.id == id {
			return i
		}
	}
	return 0
}

// Warnings contain only source names and sanitized errors, never customer records.
func warnings(data snapshot) string {
	var out []string
	for _, r := range data {
		if r.err != "" {
			out = append(out, r.title+": "+r.err)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
