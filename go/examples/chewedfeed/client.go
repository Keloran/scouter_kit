package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type record map[string]any

type source struct {
	key, title, path string
	fields           []string
	list             bool
}

type result struct {
	source
	rows []record
	at   time.Time
	err  string
}

type snapshot []result

type client struct {
	base, token string
	http        *http.Client
	project     string
	details     bool
}

func newClient(base, token, project string, details bool) (*client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("-api must be an http(s) base URL without credentials, query or fragment")
	}
	if strings.ContainsAny(token, ";\r\n\t ") {
		return nil, errors.New("token file must contain only the session JWT")
	}
	if token != "" && u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, errors.New("use HTTPS when sending a session token to a remote API")
	}
	return &client{base: strings.TrimRight(base, "/"), token: token, project: project, details: details,
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

// get never includes response bodies or request URLs in errors: either may contain private data.
func (c *client) get(ctx context.Context, s source) result {
	r := result{source: s}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+s.path, nil)
	if err != nil {
		r.err = "invalid request"
		return r
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.AddCookie(&http.Cookie{Name: "session", Value: c.token})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		r.err = "request failed or timed out"
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			r.err += " (session missing or expired)"
		}
		return r
	}
	const maxBody = 4 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		r.err = "response unreadable or over 4 MiB"
		return r
	}
	if s.list {
		err = json.Unmarshal(b, &r.rows)
	} else {
		var row record
		err = json.Unmarshal(b, &row)
		if row == nil && err == nil {
			err = errors.New("null object")
		}
		r.rows = []record{row}
	}
	if err != nil || strings.TrimSpace(string(b)) == "null" {
		r.err = "invalid JSON response"
		r.rows = nil
		return r
	}
	r.at = time.Now()
	return r
}

var projectFields = []string{"name", "stage", "health_score", "attention_mode", "github_open_prs", "description",
	"linear_progress_score", "deploy_recency_score", "github_activity_score", "task_velocity_score",
	"last_synced_at", "linear_sync_status", "tracklines_sync_status", "github_sync_status", "railway_sync_status",
	"uptime_kuma_sync_status", "sync_error_message", "links"}

// fetch bounds integration fan-out to four requests at a time. Failed sources keep their last
// successful rows with an explicit stale marker; a successful empty response clears old rows.
func (c *client) fetch(ctx context.Context, previous snapshot) snapshot {
	started := time.Now()
	progress("Fetching API project list")
	projects := c.get(ctx, source{"projects", "Projects", "/api/projects", projectFields, true})
	projects = retain(projects, previous)
	if projects.err != "" {
		progress("Project list unavailable: %s; cached projects: %d", projects.err, len(projects.rows))
	} else {
		progress("Project list loaded: %d projects", len(projects.rows))
	}
	if c.project != "" {
		var rows []record
		for _, row := range projects.rows {
			if row["slug"] == c.project {
				rows = append(rows, row)
			}
		}
		projects.rows = rows
		if len(rows) == 0 && projects.err == "" {
			projects.err = "project slug not found"
		}
	}
	projects.rows = append([]record(nil), projects.rows...)
	sort.SliceStable(projects.rows, func(i, j int) bool {
		priority := func(r record) int {
			switch r["attention_mode"] {
			case "fix":
				return 0
			case "revive":
				return 1
			default:
				return 2
			}
		}
		return priority(projects.rows[i]) < priority(projects.rows[j])
	})
	sources := []source{{"summary", "Chewedfeed", "/stats",
		[]string{"projects", "score", "deploys", "pull_requests", "customers", "paid_customers"}, false}}
	if c.token != "" {
		sources = append(sources,
			source{"health", "Portfolio health", "/api/health/stats", []string{"needs_attention_count", "average_health_score", "last_synced_at", "deploys_this_week", "open_pull_requests"}, false},
			source{"customers", "Customers", "/api/product-customers", []string{"product_slug", "name", "plan", "billing_status", "signed_up_at"}, true},
			source{"enquiries", "Enquiries", "/api/customer-enquiries", []string{"product_slug", "status", "name", "company", "message", "created_at"}, true})
	}
	if c.details {
		for _, p := range projects.rows {
			slug, _ := p["slug"].(string)
			if slug == "" {
				continue
			}
			name := value(p["name"])
			add := func(kind string, fields []string, list bool) {
				sources = append(sources, source{slug + "/" + kind, name + " / " + kind,
					"/api/projects/" + url.PathEscape(slug) + "/" + kind, fields, list})
			}
			if value(p["uptime_kuma_slug"]) != "" {
				add("uptime", []string{"overall_status", "uptime_24h", "uptime_30d", "monitors"}, false)
			}
			if c.token == "" {
				continue
			}
			if value(p["linear_project_id"]) != "" || value(p["tracklines_project_id"]) != "" {
				add("tickets", []string{"identifier", "title", "state_name", "priority", "assignee_name", "created_at"}, true)
			}
			if value(p["railway_project_id"]) != "" {
				add("services", []string{"label", "is_primary", "services"}, true)
			}
			if value(p["github_owner"]) != "" {
				add("repos", []string{"name", "description", "language", "open_pull_requests", "open_issues", "stars", "updated_at"}, true)
			}
		}
	}
	out := make(snapshot, len(sources)+1)
	progress("Fetching %d dashboard API sources (up to 4 requests at once)", len(sources))
	out[1] = projects
	var wg sync.WaitGroup
	jobs := make(chan int)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := retain(c.get(ctx, sources[i]), previous)
				if c.project != "" && (r.key == "customers" || r.key == "enquiries") {
					var filtered []record
					for _, row := range r.rows {
						if row["product_slug"] == c.project {
							filtered = append(filtered, row)
						}
					}
					r.rows = filtered
				}
				at := i + 1
				if i == 0 {
					at = 0
				}
				out[at] = r
			}
		}()
	}
	for i := range sources {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	failed := 0
	for _, r := range out {
		if r.err != "" {
			failed++
		}
	}
	progress("API refresh complete: %d/%d sources updated, %d stale or unavailable (%s)",
		len(out)-failed, len(out), failed, time.Since(started).Round(time.Millisecond))
	return out
}

func retain(r result, previous snapshot) result {
	if r.err != "" {
		for _, old := range previous {
			if old.key == r.key {
				r.rows, r.at = old.rows, old.at
				break
			}
		}
	}
	return r
}
