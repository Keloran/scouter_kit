package main

import (
	"fmt"
	"image"
	"math"
	"strings"

	"scouterkit"
)

// Original pixel icons, rendered directly so no emoji/font coverage is required.
var statusIcons = map[string][]string{
	"computer": {
		" ########### ", " #         # ", " #         # ", " #         # ",
		" #         # ", " ########### ", "     ###     ", "  #########  ",
	},
	"glasses": {
		"#           #", "#           #", " ##### ##### ", " #   ###   # ",
		" #   # #   # ", " #   # #   # ", " ##### ##### ",
	},
	"clock": {
		"  #####  ", " #     # ", "#   #   #", "#   #   #", "#   ### #",
		"#       #", "#       #", " #     # ", "  #####  ",
	},
	"book": {
		" ####   #### ", "#    # #    #", "#    ###    #", "#    # #    #",
		"#    # #    #", "#    # #    #", "#    ###    #", " ####   #### ",
	},
	"refresh": {
		"   #####     ", "  #     #  # ", " #      # ## ", " #      #### ",
		"             ", " ####      # ", " ## #      # ", " #  #     #  ", "     #####   ",
	},
	"hand": {
		"     #     ", "   # # #   ", "   # # # # ", "   # # # # ", "   ####### ",
		" # #     # ", " ###     # ", " #       # ", "  #     #  ", "   #####   ",
	},
	"download": {
		"    #    ", "    #    ", "    #    ", " #  #  # ", "  # # #  ",
		"   ###   ", "    #    ", "#       #", "#########",
	},
	"cache": {
		"#########", "#       #", "#########", " #     # ", " # ### # ",
		" #     # ", " #     # ", " ####### ",
	},
	"sample": {
		"      ## ", "      ## ", "   ## ## ", "   ## ## ", "## ## ## ",
		"## ## ## ", "## ## ## ", "#########",
	},
	"warning": {
		"     #     ", "    # #    ", "   # # #   ", "   # # #   ", "  #  #  #  ",
		"  #     #  ", " #   #   # ", "#         #", "###########",
	},
	"unknown": {
		" ##### ", "#     #", "      #", "    ## ", "   #   ", "       ", "   #   ",
	},
	"bell": {
		"    ###    ", "   #   #   ", "  #     #  ", "  #     #  ", "  #     #  ",
		" #       # ", " #       # ", "###########", "    # #    ", "     #     ",
	},
}

func (s *screen) iconSize(name string) (int, int) {
	icon := statusIcons[name]
	scale := math.Max(0.1, float64(s.footerFont.Ascent+s.footerFont.Descent)/11)
	return int(math.Round(float64(len(icon[0])) * scale)), int(math.Round(float64(len(icon)) * scale))
}

type footerRow struct {
	screen             *screen
	x, baseline, limit int
}

func (r *footerRow) gap() { r.x = min(r.limit, r.x+r.screen.footerFont.Measure(" ").Width) }

func (r *footerRow) text(text string) {
	f := r.screen.footerFont
	text = fitLine(text, max(0, r.limit-r.x), func(text string) int { return f.Measure(text).Width })
	r.x += f.DrawLine(r.screen.bitmap, text, r.x, r.baseline, scouterkit.AlignLeft, 153)
}

func (r *footerRow) icon(name string) {
	s, f := r.screen, r.screen.footerFont
	w, h := s.iconSize(name)
	if r.x+w > r.limit {
		return
	}
	scale := math.Max(0.1, float64(f.Ascent+f.Descent)/11)
	// Fractional rounding can make ascent+descent one pixel taller than a row.
	// Keep icons inside their row so mode changes cannot touch the battery row.
	top := r.baseline + min(f.Descent, f.LineSpacing-f.Ascent) - h
	for row, line := range statusIcons[name] {
		for col, pixel := range line {
			if pixel != '#' {
				continue
			}
			x0, x1 := int(math.Round(float64(col)*scale)), int(math.Round(float64(col+1)*scale))
			y0, y1 := int(math.Round(float64(row)*scale)), int(math.Round(float64(row+1)*scale))
			s.bitmap.Fill(image.Rect(r.x+x0, top+y0, r.x+x1, top+y1), 153)
		}
	}
	r.x += w
	r.gap()
}

type footerSource struct {
	icon, text string
	stale      bool
}

func sourceIndicator(status string) footerSource {
	status = footerStatus(status)
	stale := strings.HasPrefix(status, "STALE")
	status = strings.TrimSpace(strings.TrimPrefix(status, "STALE"))
	for _, item := range []struct{ prefix, icon string }{
		{"Fetched ", "download"}, {"Cached ", "cache"}, {"Sampled ", "sample"}, {"Received ", "bell"},
	} {
		if strings.HasPrefix(status, item.prefix) {
			return footerSource{icon: item.icon, text: strings.TrimPrefix(status, item.prefix), stale: stale}
		}
	}
	if status == "Unavailable" || status == "" {
		return footerSource{icon: "unknown", text: "--", stale: stale}
	}
	return footerSource{icon: "unknown", text: status, stale: stale}
}

func (s *screen) drawNavigationRow(p page, index, total int, paused bool) {
	r := footerRow{screen: s, x: textInset, baseline: s.footerBaseline, limit: s.w - textInset}
	if s.viewName == "Sites" {
		r.icon("book")
	} else if s.viewName == "Notifications" {
		r.icon("bell")
	} else {
		r.text(s.viewName)
		r.gap()
	}
	r.text(fmt.Sprintf("%d/%d", index+1, total))
	r.gap()
	if paused {
		r.icon("hand")
	} else {
		r.icon("refresh")
	}
	r.gap()
	source := sourceIndicator(p.status)
	if source.stale {
		r.icon("warning")
	}
	r.icon(source.icon)
	r.text(source.text)
}

func (s *screen) drawBatteryRow() {
	f := s.footerFont
	space := f.Measure(" ").Width
	clockText := s.now().Format("15:04")
	clockIconWidth, _ := s.iconSize("clock")
	clockWidth := clockIconWidth + space + f.Measure(clockText).Width
	preferredClockX := s.w - textInset - clockWidth - f.Measure(strings.Repeat(" ", 30)).Width
	// Keep both batteries before the clock, reserving enough room for the time.
	r := footerRow{screen: s, x: textInset, baseline: s.batteryBaseline, limit: s.w - textInset - clockWidth - space}
	r.icon("computer")
	r.text(batteryPercent(s.batteries.host))
	r.gap()
	r.icon("glasses")
	text := s.batteries.glassesText()
	if f.Measure(text).Width > r.limit-r.x {
		// Preserve both arm percentages before shortening the battery text.
		text = strings.ReplaceAll(strings.ReplaceAll(text, "L ", "L"), "R ", "R")
		text = strings.ReplaceAll(text, "  ", " ")
	}
	r.text(text)
	r.gap()
	clockX := max(r.x, preferredClockX)
	clock := footerRow{screen: s, x: clockX, baseline: s.batteryBaseline, limit: s.w - textInset}
	clock.icon("clock")
	clock.text(clockText)
}
