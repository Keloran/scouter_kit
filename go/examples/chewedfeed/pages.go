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

var labels = map[string]string{
	"health_score": "Health", "attention_mode": "Action", "github_open_prs": "Open PRs",
	"linear_progress_score": "Progress score", "deploy_recency_score": "Deploy score",
	"github_activity_score": "GitHub score", "task_velocity_score": "Velocity score",
	"paid_customers": "Paid customers", "pull_requests": "Open PRs", "deploys": "Deploys (7d)",
	"needs_attention_count": "Needs attention", "uptime_24h": "Uptime 24h %", "uptime_30d": "Uptime 30d %",
}

func label(k string) string {
	if s := labels[k]; s != "" {
		return s
	}
	return strings.ReplaceAll(k, "_", " ")
}

func recordLines(row record, fields []string) []string {
	var lines []string
	for _, k := range fields {
		v := row[k]
		if entries, ok := v.([]any); ok {
			for _, item := range entries {
				if m, ok := item.(map[string]any); ok {
					var keys []string
					switch k {
					case "links":
						keys = []string{"label", "url"}
					case "monitors":
						keys = []string{"name", "status", "uptime_24h", "uptime_30d"}
					case "services":
						keys = []string{"name", "deploy_status", "last_deploy_at"}
					}
					lines = append(lines, recordLines(m, keys)...)
					lines = append(lines, "")
				}
			}
			continue
		}
		if s := fieldValue(k, v); s != "" {
			lines = append(lines, label(k)+": "+s)
		}
	}
	return lines
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

func sitePage(row record, id, status string, data snapshot, width, rows int, measure func(string) int) page {
	title := value(row["name"])
	if title == "" {
		title = value(row["slug"])
	}
	if title == "" {
		title = "Unnamed site"
	}
	field := func(key, missing string) string {
		if s := fieldValue(key, row[key]); s != "" {
			return s
		}
		return missing
	}
	score := func(key, suffix string) string {
		if row[key] == nil {
			return "unknown"
		}
		return field(key, "unknown") + suffix
	}
	lines := []string{
		"Progress: " + score("linear_progress_score", "%"),
		"GitHub: " + score("github_activity_score", "/100") + "  PRs: " + field("github_open_prs", "unknown"),
		"Action: " + field("attention_mode", "unknown"),
		"Last sync: " + field("last_synced_at", "never"),
		"Stage: " + field("stage", "unknown"),
	}
	slug := value(row["slug"])
	for _, r := range data {
		if r.key != "customers" || slug == "" {
			continue
		}
		if r.err != "" {
			if !strings.HasPrefix(status, "STALE") {
				status = "STALE customers / " + status
			}
			if r.at.IsZero() {
				lines = append(lines, "Customers: unavailable")
				continue
			}
		}
		count, paid := 0, 0
		for _, customer := range r.rows {
			if customer["product_slug"] != slug {
				continue
			}
			count++
			if plan := value(customer["plan"]); plan != "" && plan != "free" {
				switch customer["billing_status"] {
				case "active", "trialing", "past_due":
					paid++
				}
			}
		}
		lines = append(lines, fmt.Sprintf("Customers: %d (%d paid)", count, paid))
	}
	lines = append(lines, recordLines(row, []string{"health_score"})...)
	for _, r := range data {
		if r.key == "enquiries" && slug != "" {
			if r.err != "" {
				if !strings.HasPrefix(status, "STALE") {
					status = "STALE enquiries / " + status
				}
				lines = append(lines, "Enquiries: "+r.err)
				continue
			}
			count := 0
			for _, enquiry := range r.rows {
				if enquiry["product_slug"] == slug {
					count++
				}
			}
			lines = append(lines, fmt.Sprintf("Enquiries: %d", count))
			continue
		}
		if slug == "" || !strings.HasPrefix(r.key, slug+"/") {
			continue
		}
		kind := strings.TrimPrefix(r.key, slug+"/")
		if r.err != "" {
			if !strings.HasPrefix(status, "STALE") {
				status = "STALE details / " + status
			}
			lines = append(lines, kind+": "+r.err)
			continue
		}
		switch kind {
		case "uptime":
			for _, uptime := range r.rows {
				lines = append(lines, recordLines(uptime, []string{"overall_status", "uptime_24h", "uptime_30d"})...)
			}
		case "tickets", "repos":
			lines = append(lines, fmt.Sprintf("%s: %d", label(kind), len(r.rows)))
		case "services":
			count := 0
			for _, env := range r.rows {
				if services, ok := env["services"].([]any); ok {
					count += len(services)
				}
			}
			lines = append(lines, fmt.Sprintf("Services: %d", count))
		}
	}
	lines = append(lines, recordLines(row, []string{"deploy_recency_score", "task_velocity_score", "description",
		"sync_error_message", "linear_sync_status", "tracklines_sync_status", "github_sync_status",
		"railway_sync_status", "uptime_kuma_sync_status", "links"})...)
	lines = lines[:min(rows, len(lines))]
	for i, line := range lines {
		lines[i] = fitLine(line, width, measure)
	}
	return page{"projects/" + id + "/0", title, lines, status}
}

func makePages(data snapshot, width, rows int, measure func(string) int) []page {
	var pages []page
	// Per-site integration responses enrich its card instead of adding more pages.
	details := map[string]bool{}
	for _, r := range data {
		if r.key == "projects" {
			if len(r.rows) > 0 {
				// Per-product records are summarized on their site's page.
				details["customers"] = true
				details["enquiries"] = true
			}
			for _, row := range r.rows {
				if slug := value(row["slug"]); slug != "" {
					for _, kind := range []string{"uptime", "tickets", "repos", "services"} {
						details[slug+"/"+kind] = true
					}
				}
			}
		}
	}
	for _, r := range data {
		if details[r.key] {
			continue
		}
		status := "Updated " + r.at.Local().Format("15:04")
		if r.at.IsZero() {
			status = "Unavailable"
		}
		if r.err != "" {
			status = "STALE " + status + " / " + r.err
		}
		if len(r.rows) == 0 {
			lines := []string{"No entries"}
			if r.err != "" {
				lines = wrap(r.err, width, measure)
			}
			pages = append(pages, page{r.key, r.title, lines, status})
		}
		for i, row := range r.rows {
			id := value(row["id"])
			if id == "" {
				id = value(row["slug"])
			}
			if id == "" {
				id = fmt.Sprint(i)
			}
			if r.key == "projects" {
				pages = append(pages, sitePage(row, id, status, data, width, rows, measure))
				continue
			}
			var lines []string
			for _, line := range recordLines(row, r.fields) {
				lines = append(lines, wrap(line, width, measure)...)
			}
			if len(lines) == 0 {
				lines = []string{"No data"}
			}
			for n := 0; n < len(lines); n += rows {
				pages = append(pages, page{fmt.Sprintf("%s/%s/%d", r.key, id, n), r.title,
					lines[n:min(n+rows, len(lines))], status})
			}
		}
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
