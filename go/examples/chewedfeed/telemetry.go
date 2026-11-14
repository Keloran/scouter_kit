package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	h "scouterkit/examples/internal/handover"
)

// Laptop telemetry for the phone (tests/handover policy.json "telemetry"): AI usage windows and aggregate
// system figures, each source off unless the user shares it. Only the schema's fields leave the process: no
// plan or account metadata, process lists, paths, commands or identifiers. Readings carry their sample time;
// the phone ages them itself, so a suspended laptop shows stale data, never fresh zeros.
const (
	aiStaleMs     = 300000 // policy.json timers_ms.ai_stale
	systemStaleMs = 90000  // timers_ms.telemetry_stale
)

// aiWindowKind maps the desktop's window names to the schema's short / weekly / other.
func aiWindowKind(name string) string {
	switch name {
	case "Session", "5h":
		return "short"
	case "Weekly":
		return "weekly"
	}
	return "other"
}

// aiTelemetry reads the ai-usagebar caches under dir (read only). Readable providers contribute their windows;
// the sample time is the oldest readable cache's; none readable is unavailable.
func aiTelemetry(dir string, now time.Time) h.Telemetry {
	providers := []struct {
		name, directory string
		decode          func([]byte) ([]usageWindow, []string, error)
	}{{"codex", "openai", openAIUsage}, {"claude", "anthropic", claudeUsage}}
	var windows []h.AIWindow
	var oldest time.Time
	for _, p := range providers {
		if dir == "" {
			break
		}
		data, updated, err := readUsageCache(filepath.Join(dir, p.directory, "usage.json"))
		if err != nil {
			continue
		}
		ws, _, err := p.decode(data) // the second value is plan / account detail: never shared
		if err != nil {
			continue
		}
		if oldest.IsZero() || updated.Before(oldest) {
			oldest = updated
		}
		for _, w := range ws {
			if len(windows) == 8 {
				break
			}
			a := h.AIWindow{Provider: p.name, Window: aiWindowKind(w.name)}
			if w.used != nil && *w.used >= 0 && *w.used <= 100000 {
				u := *w.used
				a.UsedPercent = &u
			}
			if !w.reset.IsZero() && w.reset.UnixMilli() > 0 {
				r := uint64(w.reset.UnixMilli())
				a.ResetUnixMs = &r
			}
			windows = append(windows, a)
		}
	}
	if oldest.IsZero() {
		return h.Telemetry{SchemaVersion: 1, Source: "ai", Status: "unavailable"}
	}
	values, _ := json.Marshal(windows)
	return sampled("ai", oldest, now, aiStaleMs, values)
}

// systemTelemetry: aggregate CPU and memory from the sampler, the laptop's battery. No processes.
func systemTelemetry(s *systemSample, battery *int, now time.Time) h.Telemetry {
	if s == nil {
		return h.Telemetry{SchemaVersion: 1, Source: "system", Status: "unavailable"}
	}
	v := h.SystemValues{MemoryUsedBytes: s.memoryUsed, MemoryTotalBytes: s.memoryTotal}
	if v.MemoryUsedBytes > v.MemoryTotalBytes {
		v.MemoryUsedBytes = v.MemoryTotalBytes
	}
	if s.cpu != nil && *s.cpu >= 0 && *s.cpu <= 100 {
		c := *s.cpu
		v.CPUPercent = &c
	}
	if battery != nil && *battery >= 0 && *battery <= 100 {
		b := *battery
		v.BatteryPercent = &b
	}
	values, _ := json.Marshal(v)
	return sampled("system", s.at, now, systemStaleMs, values)
}

func sampled(source string, at, now time.Time, staleMs int64, values json.RawMessage) h.Telemetry {
	age := max(0, now.Sub(at).Milliseconds())
	t := uint64(max(0, at.UnixMilli()))
	status := "fresh"
	if age >= staleMs {
		status = "stale"
	}
	return h.Telemetry{SchemaVersion: 1, Source: source, SampleUnixMs: &t, AgeMs: uint64(age), Status: status, Values: values}
}

// telemetryFeed numbers each source's readings: the revision moves only when the values, the sample time or the
// status change (an unchanged reading is not sent again).
type telemetryFeed struct {
	revision map[string]uint64
	last     map[string]h.Telemetry
}

func (f *telemetryFeed) next(t h.Telemetry) (h.Telemetry, bool) {
	if f.revision == nil {
		f.revision, f.last = map[string]uint64{}, map[string]h.Telemetry{}
	}
	prev, seen := f.last[t.Source]
	if seen && prev.Status == t.Status && bytes.Equal(prev.Values, t.Values) && equalTime(prev.SampleUnixMs, t.SampleUnixMs) {
		return prev, false
	}
	f.revision[t.Source]++
	t.Revision = f.revision[t.Source]
	f.last[t.Source] = t
	return t, true
}

func equalTime(a, b *uint64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// systemEvery bounds how often system readings go to the phone (the sampler runs every 2 s); a status change
// goes at once. Well inside the phone's 90 s staleness.
const systemEvery = 30 * time.Second

// publishTelemetry samples the shared sources and hands each new reading to send, until ctx ends. The AI
// caches are read every aiEvery; system readings go at most every systemEvery.
func publishTelemetry(ctx context.Context, shareAI, shareSystem bool, aiDir string, aiEvery time.Duration, send func(h.Telemetry)) {
	if !shareAI && !shareSystem {
		return
	}
	var feed telemetryFeed
	var system <-chan systemUpdate
	if shareSystem {
		system = monitorSystem(ctx, "/proc")
	}
	var lastSystem time.Time
	aiTick := time.NewTicker(aiEvery)
	defer aiTick.Stop()
	publish := func(t h.Telemetry) {
		if t, changed := feed.next(t); changed {
			send(t)
		}
	}
	if shareAI {
		publish(aiTelemetry(aiDir, time.Now()))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-system:
			if !ok {
				system = nil
				continue
			}
			var sample *systemSample
			if u.err == nil {
				sample = u.sample
			}
			now := time.Now()
			t := systemTelemetry(sample, hostBattery("/sys/class/power_supply"), now)
			if prev, seen := feed.last["system"]; seen && prev.Status == t.Status && now.Sub(lastSystem) < systemEvery {
				continue
			}
			lastSystem = now
			publish(t)
		case <-aiTick.C:
			if shareAI {
				publish(aiTelemetry(aiDir, time.Now()))
			}
		}
	}
}
