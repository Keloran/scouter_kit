package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func processStat(pid int, name string, ticks, start, rss int) string {
	f := strings.Fields("R 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")
	f[11], f[12], f[19], f[21] = fmt.Sprint(ticks), "0", fmt.Sprint(start), fmt.Sprint(rss)
	return fmt.Sprintf("%d (%s) %s", pid, name, strings.Join(f, " "))
}

func writeSystemFile(t *testing.T, root, path, contents string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSystemUsageDeltasPagesAndStaleData(t *testing.T) {
	root := t.TempDir()
	stat := "cpu 100 0 50 850 0 0 0 0 500 0\ncpu0 0\ncpu1 0\n"
	for path, data := range map[string]string{
		"stat":    stat,
		"meminfo": "MemTotal: 1048576 kB\nMemAvailable: 524288 kB\nSwapTotal: 1024 kB\nSwapFree: 512 kB\n",
		"loadavg": "0.10 0.20 0.30 1/10 42\n", "uptime": "90061.0 0.0\n",
		"42/stat": processStat(42, "worker (alpha)", 100, 123, 10),
		"43/stat": processStat(43, "memory", 10, 124, 1000),
	} {
		writeSystemFile(t, root, path, data)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	first, err := readSystemSample(root, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.cpu != nil || first.cores != 2 || first.totalTicks != 1000 || first.uptime != 90061*time.Second {
		t.Fatalf("initial CPU sample or uptime: %+v", first)
	}
	writeSystemFile(t, root, "stat", "cpu 180 0 70 950 0 0 0 0 500 0\ncpu0 0\ncpu1 0\n")
	writeSystemFile(t, root, "42/stat", processStat(42, "worker (alpha)", 120, 123, 10))
	writeSystemFile(t, root, "43/stat", processStat(43, "memory", 12, 124, 1000))
	second, err := readSystemSample(root, first, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.cpu == nil || *second.cpu != 50 || second.memoryUsed != 512*1024*1024 || second.swapUsed != 512*1024 {
		t.Fatalf("incorrect CPU or memory counters: %+v", second)
	}
	if len(second.processes) != 2 || second.processes[0].cpu == nil || *second.processes[0].cpu != 20 {
		t.Fatalf("per-core process CPU percentage: %+v", second.processes)
	}
	pages := systemPages(systemUpdate{sample: second}, 10)
	if len(pages) != 1 || !strings.Contains(pages[0].lines[4], "worker (alpha)") || !strings.Contains(pages[0].lines[5], "memory") {
		t.Fatalf("CPU ordering: %+v", pages)
	}
	for _, p := range systemPages(systemUpdate{sample: second}, 4) {
		if len(p.lines) > 4 {
			t.Fatal("system page exceeded display row budget")
		}
	}
	writeSystemFile(t, root, "stat", "cpu 200 0 70 1030 0 0 0 0 500 0\ncpu0 0\ncpu1 0\n")
	writeSystemFile(t, root, "42/stat", processStat(42, "replacement", 130, 999, 10))
	reused, err := readSystemSample(root, second, now.Add(3*time.Second))
	if err != nil || reused.processes[0].cpu != nil {
		t.Fatalf("reused PID inherited the previous process CPU: %+v (%v)", reused, err)
	}
	if err := os.Remove(filepath.Join(root, "uptime")); err != nil {
		t.Fatal(err)
	}
	_, err = readSystemSample(root, second, now.Add(4*time.Second))
	if err == nil {
		t.Fatal("missing system data was accepted")
	}
	stale := systemPages(systemUpdate{sample: second, err: err}, 10)
	if stale[0].status != "STALE Sampled 12:00:02" || stale[0].lines[0] != pages[0].lines[0] {
		t.Fatal("failed refresh did not preserve and mark the last successful reading")
	}
	if got := systemPages(systemUpdate{err: err}, 10); len(got) == 0 || got[0].status != "Unavailable" {
		t.Fatal("missing initial system information has no fallback")
	}
}

func TestProcessStatNamesAndPIDReuse(t *testing.T) {
	p, err := parseProcessStat(processStat(42, "name (with) spaces", 100, 123, 10), 4096)
	if err != nil || p.name != "name (with) spaces" || p.memory != 40960 || p.start != 123 || p.ticks != 100 {
		t.Fatalf("process stat parsing: %+v (%v)", p, err)
	}
	for _, data := range []string{"", "42 (bad)", "not-a-pid (name) " + strings.Repeat("0 ", 22)} {
		if _, err := parseProcessStat(data, 4096); err == nil {
			t.Fatalf("invalid process record accepted: %q", data)
		}
	}
}

func TestSystemShowsOnlyTopFiveProcessesByCPU(t *testing.T) {
	s := &systemSample{at: time.Now(), cores: 8}
	for i := 0; i < 8; i++ {
		cpu := float64(i * 10)
		s.processes = append(s.processes, processUsage{pid: i + 1, name: fmt.Sprintf("process-%d", i), cpu: &cpu, memory: uint64(100 - i)})
	}
	pages := systemPages(systemUpdate{sample: s}, 30)
	if len(pages) != 1 {
		t.Fatalf("expected one system page, got %d", len(pages))
	}
	processes := pages[0].lines[4:]
	if len(processes) != 5 {
		t.Fatalf("expected five processes, got %d", len(processes))
	}
	for i, line := range processes {
		if !strings.HasSuffix(line, fmt.Sprintf("process-%d", 7-i)) {
			t.Fatalf("CPU sort incorrect: %v", processes)
		}
	}
}
