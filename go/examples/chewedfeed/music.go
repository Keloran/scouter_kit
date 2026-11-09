package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	musicRefresh           = 2 * time.Second
	musicScrollEvery       = 250 * time.Millisecond
	musicMinimumTitleTime  = 5 * time.Second
	musicTimeDisplay       = 5 * time.Second
	musicStatusFormat      = "Mon 2 Jan 15:04"
	musicStatusSeparator   = "\x1f"
	musicScrollGap         = 3
	musicScrollsBeforeTime = 3
)

// currentMusicTitle reads the first playing MPRIS player's title through
// playerctl. It only queries metadata; no playback controls are sent.
func currentMusicTitle(parent context.Context) string {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	format := "{{status}}" + musicStatusSeparator + "{{title}}"
	out, err := exec.CommandContext(ctx, "playerctl", "--all-players", "--format", format, "metadata").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, musicStatusSeparator, 2)
		if len(fields) == 2 && fields[0] == "Playing" {
			if title := value(fields[1]); title != "" {
				return title
			}
		}
	}
	return ""
}

type musicStatus struct {
	title           string
	showingTime     bool
	titleStarted    time.Time
	timeUntil       time.Time
	nextScroll      time.Time
	scrollOffset    int
	completedScroll int
}

func (m *musicStatus) setTitle(title string, now time.Time) bool {
	if title == m.title {
		return false
	}
	m.title = title
	m.showingTime = title == ""
	m.titleStarted = now
	m.timeUntil = time.Time{}
	m.nextScroll = now.Add(musicScrollEvery)
	m.scrollOffset = 0
	m.completedScroll = 0
	return true
}

func (m *musicStatus) advance(now time.Time, width int, measure func(string) int) bool {
	if m.title == "" {
		return false
	}
	if m.showingTime {
		if now.Before(m.timeUntil) {
			return false
		}
		m.showingTime = false
		m.titleStarted = now
		m.nextScroll = now.Add(musicScrollEvery)
		m.scrollOffset, m.completedScroll = 0, 0
		return true
	}

	scrolling := measure(m.title) > width
	changed := false
	if scrolling && !now.Before(m.nextScroll) {
		steps := int(now.Sub(m.nextScroll)/musicScrollEvery) + 1
		cycle := len([]rune(m.title)) + musicScrollGap
		position := m.scrollOffset + steps
		m.completedScroll += position / cycle
		m.scrollOffset = position % cycle
		m.nextScroll = m.nextScroll.Add(time.Duration(steps) * musicScrollEvery)
		changed = true
	}
	readyForTime := !now.Before(m.titleStarted.Add(musicMinimumTitleTime))
	if readyForTime && (!scrolling || m.completedScroll >= musicScrollsBeforeTime) {
		m.showingTime = true
		m.timeUntil = now.Add(musicTimeDisplay)
		return true
	}
	return changed
}

func (m *musicStatus) line(now time.Time, width int, measure func(string) int) string {
	if m.title == "" || m.showingTime {
		return now.Format(musicStatusFormat)
	}
	runes := []rune(m.title)
	if measure(m.title) <= width {
		return m.title
	}
	probe := strings.Repeat("M", 1)
	visible := 0
	for visible < 4096 && measure(probe+strings.Repeat("M", visible)) <= width {
		visible++
	}
	cycle := len(runes) + musicScrollGap
	start := m.scrollOffset % cycle
	stream := make([]rune, 0, (len(runes)+musicScrollGap)*3+visible)
	for len(stream) < start+visible {
		stream = append(stream, runes...)
		stream = append(stream, []rune(strings.Repeat(" ", musicScrollGap))...)
	}
	stream = stream[start:]
	line := make([]rune, 0, visible)
	for _, r := range stream {
		candidate := string(append(line, r))
		if measure(candidate) > width {
			break
		}
		line = append(line, r)
	}
	return string(line)
}

func musicStatusLine(title string, showMusic bool, now time.Time) string {
	if title != "" && showMusic {
		return title
	}
	return now.Format(musicStatusFormat)
}
