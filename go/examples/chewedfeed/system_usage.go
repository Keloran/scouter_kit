package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type processUsage struct {
	pid                  int
	name, state          string
	start, ticks, memory uint64
	cpu                  *float64
}

type systemSample struct {
	at                                           time.Time
	totalTicks, idleTicks                        uint64
	cores                                        int
	cpu                                          *float64
	memoryTotal, memoryUsed, swapTotal, swapUsed uint64
	load                                         string
	uptime                                       time.Duration
	processes                                    []processUsage
}

type systemUpdate struct {
	sample *systemSample
	err    error
}

func parseCPUStat(data string) (total, idle uint64, cores int, err error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	fields := strings.Fields(lines[0])
	if len(fields) < 9 || fields[0] != "cpu" {
		return 0, 0, 0, fmt.Errorf("CPU counters unavailable")
	}
	// Guest counters are already included in user/nice; count only the first eight.
	for i, field := range fields[1:9] {
		n, e := strconv.ParseUint(field, 10, 64)
		if e != nil {
			return 0, 0, 0, fmt.Errorf("invalid CPU counter")
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) > 0 && strings.HasPrefix(f[0], "cpu") {
			if _, e := strconv.Atoi(strings.TrimPrefix(f[0], "cpu")); e == nil {
				cores++
			}
		}
	}
	return total, idle, max(1, cores), nil
}

func parseProcessStat(data string, pageSize uint64) (processUsage, error) {
	var p processUsage
	open, close := strings.IndexByte(data, '('), strings.LastIndexByte(data, ')')
	if open < 1 || close <= open {
		return p, fmt.Errorf("invalid process stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(data[:open]))
	f := strings.Fields(data[close+1:])
	if err != nil || pid < 1 || len(f) < 22 {
		return p, fmt.Errorf("incomplete process stat")
	}
	numbers := make([]uint64, 3)
	for i, index := range []int{11, 12, 19} { // utime, stime, starttime: fields 14,15,22
		numbers[i], err = strconv.ParseUint(f[index], 10, 64)
		if err != nil {
			return p, fmt.Errorf("invalid process counter")
		}
	}
	rss, err := strconv.ParseInt(f[21], 10, 64) // field 24: resident pages
	if err != nil {
		return p, fmt.Errorf("invalid process memory")
	}
	return processUsage{pid: pid, name: value(data[open+1 : close]), state: f[0],
		ticks: numbers[0] + numbers[1], start: numbers[2], memory: uint64(max(0, rss)) * pageSize}, nil
}

func readSystemSample(root string, previous *systemSample, now time.Time) (*systemSample, error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return "", fmt.Errorf("system %s unavailable", name)
		}
		return string(b), nil
	}
	stat, err := read("stat")
	if err != nil {
		return nil, err
	}
	s := &systemSample{at: now}
	s.totalTicks, s.idleTicks, s.cores, err = parseCPUStat(stat)
	if err != nil {
		return nil, err
	}
	meminfo, err := read("meminfo")
	if err != nil {
		return nil, err
	}
	memory := make(map[string]uint64)
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			if n, e := strconv.ParseUint(f[1], 10, 64); e == nil {
				memory[strings.TrimSuffix(f[0], ":")] = n * 1024
			}
		}
	}
	s.memoryTotal, s.swapTotal = memory["MemTotal"], memory["SwapTotal"]
	if s.memoryTotal == 0 {
		return nil, fmt.Errorf("memory counters unavailable")
	}
	available, ok := memory["MemAvailable"]
	if !ok {
		return nil, fmt.Errorf("available memory counter unavailable")
	}
	s.memoryUsed = s.memoryTotal - min(s.memoryTotal, available)
	s.swapUsed = s.swapTotal - min(s.swapTotal, memory["SwapFree"])
	load, err := read("loadavg")
	if err != nil {
		return nil, err
	}
	f := strings.Fields(load)
	if len(f) < 3 {
		return nil, fmt.Errorf("load averages unavailable")
	}
	s.load = strings.Join(f[:3], " ")
	uptime, err := read("uptime")
	if err != nil {
		return nil, err
	}
	f = strings.Fields(uptime)
	if len(f) == 0 {
		return nil, fmt.Errorf("uptime unavailable")
	}
	seconds, err := strconv.ParseFloat(f[0], 64)
	if err != nil || seconds < 0 {
		return nil, fmt.Errorf("invalid uptime")
	}
	s.uptime = time.Duration(seconds * float64(time.Second))
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("process list unavailable")
	}
	old := make(map[int]processUsage)
	var delta uint64
	if previous != nil && s.totalTicks > previous.totalTicks && s.idleTicks >= previous.idleTicks {
		delta = s.totalTicks - previous.totalTicks
		idle := min(delta, s.idleTicks-previous.idleTicks)
		cpu := 100 * float64(delta-idle) / float64(delta)
		s.cpu = &cpu
		for _, p := range previous.processes {
			old[p.pid] = p
		}
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue // A process may exit, or be inaccessible, between listing and reading.
		}
		p, err := parseProcessStat(string(data), uint64(os.Getpagesize()))
		if err != nil {
			continue
		}
		if prev, ok := old[p.pid]; ok && prev.start == p.start && p.ticks >= prev.ticks && delta > 0 {
			cpu := 100 * float64(p.ticks-prev.ticks) * float64(s.cores) / float64(delta)
			p.cpu = &cpu
		}
		s.processes = append(s.processes, p)
	}
	return s, nil
}

func monitorSystem(ctx context.Context, root string) <-chan systemUpdate {
	updates := make(chan systemUpdate, 1)
	go func() {
		defer close(updates)
		var previous *systemSample
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			sample, err := readSystemSample(root, previous, time.Now())
			if err == nil {
				previous = sample
			}
			select {
			case updates <- systemUpdate{sample: previous, err: err}:
			case <-ctx.Done():
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return updates
}

func systemPages(update systemUpdate, rows int) []page {
	s := update.sample
	if s == nil {
		return []page{{id: "system/cpu", title: "System monitor", lines: []string{"System information unavailable"}, status: "Unavailable"}}
	}
	status := "Sampled " + s.at.Format("15:04:05")
	if update.err != nil {
		status = "STALE " + status
	}
	percent := func(n *float64) string {
		if n == nil {
			return "sampling"
		}
		return fmt.Sprintf("%.1f%%", *n)
	}
	gib := func(n uint64) float64 { return float64(n) / (1024 * 1024 * 1024) }
	running := 0
	for _, p := range s.processes {
		if p.state == "R" {
			running++
		}
	}
	id, title := "system/cpu", "System / top 5 CPU"
	lines := []string{
		fmt.Sprintf("CPU: %s (%d cores)", percent(s.cpu), s.cores),
		fmt.Sprintf("RAM: %.1f/%.1f GiB", gib(s.memoryUsed), gib(s.memoryTotal)),
		fmt.Sprintf("Swap: %.1f/%.1f GiB", gib(s.swapUsed), gib(s.swapTotal)),
		"Load: " + s.load,
		"Uptime: " + shortDuration(s.uptime),
		fmt.Sprintf("Tasks: %d / %d running", len(s.processes), running),
	}
	lines = append(lines, "CPU%   RAM    PID NAME")
	processes := append([]processUsage(nil), s.processes...)
	cpu := func(p processUsage) float64 {
		if p.cpu == nil {
			return -1
		}
		return *p.cpu
	}
	sort.Slice(processes, func(i, j int) bool {
		a, b := processes[i], processes[j]
		if cpu(a) != cpu(b) {
			return cpu(a) > cpu(b)
		}
		if a.memory != b.memory {
			return a.memory > b.memory
		}
		return a.pid < b.pid
	})
	for _, p := range processes[:min(5, len(processes))] {
		if len(lines) >= rows {
			break
		}
		usage := "   -"
		if p.cpu != nil {
			usage = fmt.Sprintf("%4.1f", *p.cpu)
		}
		lines = append(lines, fmt.Sprintf("%s %5.0fM %6d %s", usage, float64(p.memory)/(1024*1024), p.pid, p.name))
	}
	return []page{{id: id, title: title, lines: lines[:min(len(lines), max(1, rows))], status: status}}
}
