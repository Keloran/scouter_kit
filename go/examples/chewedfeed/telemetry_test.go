package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"scouterkit/examples/internal/localcontrol"

	h "scouterkit/examples/internal/handover"
)

// What the phone would receive: the message must pass the protocol's own checks.
func telemetryOnTheWire(t *testing.T, tm h.Telemetry) h.Message {
	t.Helper()
	o := h.Outbox{Sender: strings.Repeat("a", 64), Recipient: strings.Repeat("b", 64), Session: strings.Repeat("c", 32), PeerSession: strings.Repeat("d", 32)}
	m, err := o.Next("telemetry", 0, "", h.TelemetryBody{Telemetry: tm})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	got, err := h.DecodeMessage(b)
	if err != nil {
		t.Fatalf("not a valid telemetry message: %v\n%s", err, b)
	}
	return got
}

func TestAITelemetry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	openai := fmt.Sprintf(`{"plan_type":"plus","private_metadata":"private-marker","rate_limit":{
"primary_window":{"used_percent":20.5,"limit_window_seconds":18000,"reset_at":%d},
"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":%d}}}`,
		now.Add(time.Hour).Unix(), now.Add(6*24*time.Hour).Unix())
	claude := fmt.Sprintf(`{"five_hour":{"utilization":60,"resets_at":%q},"seven_day_opus":{"utilization":5,"resets_at":%q},
"extra_usage":{"is_enabled":true},"account":"someone@example.invalid"}`,
		now.Add(time.Hour).Format(time.RFC3339), now.Add(6*24*time.Hour).Format(time.RFC3339))
	writeUsage(t, dir, "openai", openai, now.Add(-time.Minute))
	writeUsage(t, dir, "anthropic", claude, now.Add(-2*time.Minute))

	tm := aiTelemetry(dir, now)
	m := telemetryOnTheWire(t, tm)
	body := string(m.Body)
	for _, private := range []string{"plus", "private-marker", "Extra", "someone", "Plan", dir} {
		if strings.Contains(body, private) {
			t.Fatalf("%q left the laptop: %s", private, body)
		}
	}
	var windows []h.AIWindow
	json.Unmarshal(tm.Values, &windows)
	got := make([]string, 0, len(windows))
	for _, w := range windows {
		got = append(got, fmt.Sprintf("%s/%s/%.1f", w.Provider, w.Window, *w.UsedPercent))
	}
	if strings.Join(got, " ") != "codex/short/20.5 codex/weekly/0.0 claude/short/60.0 claude/other/5.0" {
		t.Fatalf("windows: %v", got)
	}
	if tm.Status != "fresh" || tm.AgeMs != 120000 || *tm.SampleUnixMs != uint64(now.Add(-2*time.Minute).UnixMilli()) {
		t.Fatalf("the oldest cache dates the reading: %+v", tm)
	}
	if aiTelemetry(dir, now.Add(5*time.Minute)).Status != "stale" {
		t.Fatal("an old cache was sent as fresh")
	}
	writeUsage(t, dir, "openai", `{`, now) // one unreadable cache: the other still goes
	if tm := aiTelemetry(dir, now); !strings.Contains(string(tm.Values), "claude") || strings.Contains(string(tm.Values), "codex") {
		t.Fatalf("partial failure: %s", tm.Values)
	}
	none := aiTelemetry(t.TempDir(), now)
	telemetryOnTheWire(t, none)
	if none.Status != "unavailable" || none.Values != nil || none.SampleUnixMs != nil {
		t.Fatalf("no cache: %+v", none)
	}
}

func TestSystemTelemetry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if u := systemTelemetry(nil, nil, now); u.Status != "unavailable" || u.Values != nil {
		t.Fatalf("no sample: %+v", u)
	}
	cpu, battery := 12.5, 75
	s := &systemSample{at: now.Add(-3 * time.Second), cpu: &cpu, memoryUsed: 9, memoryTotal: 8, processes: []processUsage{{pid: 1, name: "secret-process"}}}
	tm := systemTelemetry(s, &battery, now)
	m := telemetryOnTheWire(t, tm)
	if strings.Contains(string(m.Body), "secret-process") || strings.Contains(string(m.Body), `"pid"`) {
		t.Fatal("process details left the laptop")
	}
	var v h.SystemValues
	json.Unmarshal(tm.Values, &v)
	if *v.CPUPercent != 12.5 || *v.BatteryPercent != 75 || v.MemoryUsedBytes != 8 || tm.AgeMs != 3000 || tm.Status != "fresh" {
		t.Fatalf("values: %+v %+v", v, tm)
	}
	s.cpu = nil // the sampler's first reading has no CPU yet
	if tm := systemTelemetry(s, nil, now.Add(2*time.Minute)); tm.Status != "stale" || strings.Contains(string(tm.Values), "12.5") {
		t.Fatalf("stale or invented: %+v %s", tm, tm.Values)
	}
}

func TestTelemetryRevisionsOnlyOnChange(t *testing.T) {
	var f telemetryFeed
	at := uint64(1)
	a := h.Telemetry{Source: "system", SampleUnixMs: &at, Status: "fresh", Values: json.RawMessage(`{"x":1}`)}
	first, changed := f.next(a)
	if !changed || first.Revision != 1 {
		t.Fatal("first reading")
	}
	if _, changed := f.next(a); changed {
		t.Fatal("unchanged reading sent again")
	}
	a.Status = "stale"
	if b, changed := f.next(a); !changed || b.Revision != 2 {
		t.Fatal("status change not a new revision")
	}
	ai := h.Telemetry{Source: "ai", Status: "unavailable"}
	if c, _ := f.next(ai); c.Revision != 1 {
		t.Fatal("revisions are per source")
	}
}

// Telemetry goes only to a phone that negotiated it, the newest reading again on a new link, and it never
// changes ownership.
func TestTelemetryOverTheLink(t *testing.T) {
	n := &ownershipTestNative{}
	c, _, clock := startOwnershipTest(t, n, true, true)
	plain := &ownershipTestLink{}
	plain.onClose = func() { c.packets <- ownershipPacket{link: plain, down: true} }
	c.packets <- ownershipPacket{link: plain}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Eligible && s.Phase == string(h.Recovering) }) // started
	time.Sleep(50 * time.Millisecond)
	at := uint64(1)
	before := c.Status()
	c.Telemetry(h.Telemetry{SchemaVersion: 1, Revision: 1, Source: "system", SampleUnixMs: &at, AgeMs: 1000, Status: "fresh",
		Values: json.RawMessage(`{"cpu_percent":null,"memory_used_bytes":1,"memory_total_bytes":2,"battery_percent":null}`)})
	time.Sleep(50 * time.Millisecond)
	if len(plain.messages("telemetry")) != 0 {
		t.Fatal("telemetry sent to a phone that did not negotiate it")
	}
	if after := c.Status(); after.Owner != before.Owner || after.Phase != before.Phase {
		t.Fatalf("telemetry changed ownership: %+v -> %+v", before, after)
	}
	plain.Close()
	time.Sleep(50 * time.Millisecond)
	clock.advance(2 * time.Second)
	l := &ownershipTestLink{}
	c.packets <- ownershipPacket{link: l, telemetry: true}
	deadline := time.Now().Add(3 * time.Second)
	for len(l.messages("telemetry")) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	sent := l.messages("telemetry")
	if len(sent) != 1 {
		t.Fatalf("newest reading not sent on the new link: %d", len(sent))
	}
	var b h.TelemetryBody
	json.Unmarshal(sent[0].Body, &b)
	if b.Telemetry.AgeMs < 3000 {
		t.Fatalf("age not brought up to date: %d", b.Telemetry.AgeMs)
	}
}
