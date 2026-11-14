package main

import (
	"fmt"
	"image"
	"math"
	"strings"
	"time"

	"scouterkit"
)

// Original pixel icons, rendered directly so no emoji/font coverage is required.
var statusIcons = map[string][]string{
	"computer": {
		" ########### ", " #         # ", " #         # ", " #         # ",
		" #         # ", " ########### ", "     ###     ", "  #########  ",
	},
	"case": {
		"  #########  ", " #         # ", "#############", "#           #",
		"#     #     #", "#           #", " #         # ", "  #########  ",
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
	// Charging: shown in place of the low-battery warning, no wider than it.
	"charging": {
		"   ##", "  ## ", " ##  ", "#####", "  ## ", " ##  ", "##   ", "#    ",
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

func (s *screen) drawIconAt(name string, x, baseline int, gray uint8) int {
	f := s.footerFont
	icon := statusIcons[name]
	w, h := s.iconSize(name)
	scale := math.Max(0.1, float64(f.Ascent+f.Descent)/11)
	top := baseline + min(f.Descent, f.LineSpacing-f.Ascent) - h
	for row, line := range icon {
		for col, pixel := range line {
			if pixel != '#' {
				continue
			}
			x0, x1 := int(math.Round(float64(col)*scale)), int(math.Round(float64(col+1)*scale))
			y0, y1 := int(math.Round(float64(row)*scale)), int(math.Round(float64(row+1)*scale))
			s.bitmap.Fill(image.Rect(x+x0, top+y0, x+x1, top+y1), gray)
		}
	}
	return w
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
	s := r.screen
	w, _ := s.iconSize(name)
	if r.x+w > r.limit {
		return
	}
	s.drawIconAt(name, r.x, r.baseline, 153)
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

// footerSplit is the divider after "Page N/M": a third of the width, or wider
// when the font needs it.
func (s *screen) footerSplit() int {
	need := textInset + s.footerFont.Measure("Page 99/99").Width + textInset
	return min(max(s.w/3, need), s.w*2/3)
}

func (s *screen) drawNavigationRow(index, total int) {
	baseline := s.footerBaseline
	footerTop := baseline - s.footerFont.Ascent - 5
	footerBottom := baseline + s.footerFont.Descent
	split := s.footerSplit()
	s.bitmap.Fill(image.Rect(split, footerTop, split+1, footerBottom), 153)
	left := footerRow{screen: s, x: textInset, baseline: baseline, limit: split}
	left.text(fmt.Sprintf("Page %d/%d", index+1, total))
	middle := footerRow{screen: s, x: split + textInset, baseline: baseline, limit: s.w - textInset}
	statusWidth := middle.limit - middle.x
	label := s.status.line(time.Now(), statusWidth, func(text string) int { return s.footerFont.Measure(text).Width })
	middleWidth := s.footerFont.Measure(label).Width
	if middleWidth < middle.limit-middle.x {
		middle.x += (middle.limit - middle.x - middleWidth) / 2
	}
	middle.text(label)
}

const fetchGap = 10 // px between the mode icon and the fetch time

func (s *screen) drawStatusPanel(paused bool, status string) {
	divider := s.statusX - 1
	footerTop := s.footerBaseline - s.footerFont.Ascent - 5
	s.bitmap.Fill(image.Rect(divider, 0, divider+1, footerTop), 153)
	s.bitmap.Fill(image.Rect(0, footerTop, s.w, footerTop+1), 153)
	iconW, iconH := s.iconSize("glasses")
	iconX := s.statusX + (s.statusW-iconW)/2
	iconBaseline := s.footerFont.Ascent + iconH + 2
	rightBaseline := iconBaseline + 2*s.footerFont.LineSpacing
	panelMid := rightBaseline + s.footerFont.Descent + 5
	panelLower := (panelMid + footerTop) / 2
	s.bitmap.Fill(image.Rect(s.statusX, panelMid, s.w, panelMid+1), 153)
	s.bitmap.Fill(image.Rect(s.statusX, panelLower, s.w, panelLower+1), 153)

	s.drawIconAt("glasses", iconX, iconBaseline, 255)
	s.drawBatteryValue("L", s.batteryValue(scouterkit.Left), iconBaseline+s.footerFont.LineSpacing)
	s.drawBatteryValue("R", s.batteryValue(scouterkit.Right), rightBaseline)

	// Case and computer: icon, then the battery, one row each.
	middleTop := panelMid + (panelLower-panelMid-2*s.footerFont.LineSpacing)/2
	caseBaseline := middleTop + s.footerFont.Ascent
	s.drawIconBattery("case", s.batteries.casePct, s.batteries.caseCharging, caseBaseline)
	s.drawIconBattery("computer", s.batteries.host, s.batteries.hostCharging, caseBaseline+s.footerFont.LineSpacing)

	modeIcon := "refresh"
	if paused {
		modeIcon = "hand"
	}
	// Fetched data (Sites download, AI cache): its time under the mode icon.
	src := sourceIndicator(status)
	fetched := src.icon == "download" || src.icon == "cache"
	bottomHeight := footerTop - panelLower
	modeW, modeH := s.iconSize(modeIcon)
	content, gap := modeH, min(fetchGap, max(0, bottomHeight-modeH-s.footerFont.LineSpacing))
	if fetched {
		content += gap + s.footerFont.LineSpacing
	}
	modeX := s.statusX + (s.statusW-modeW)/2
	modeTop := panelLower + (bottomHeight-content)/2
	modeBaseline := modeTop + modeH - min(s.footerFont.Descent, s.footerFont.LineSpacing-s.footerFont.Ascent)
	s.drawIconAt(modeIcon, modeX, modeBaseline, 255)
	if fetched {
		iconW, _ := s.iconSize(src.icon)
		space := s.footerFont.Measure(" ").Width
		x := s.statusX + (s.statusW-iconW-space-s.footerFont.Measure(src.text).Width)/2
		baseline := modeBaseline + gap + s.footerFont.LineSpacing
		x += s.drawIconAt(src.icon, x, baseline, 255) + space
		s.footerFont.DrawLine(s.bitmap, src.text, x, baseline, scouterkit.AlignLeft, 255)
	}
}

func (s *screen) batteryFor(arm scouterkit.Arm) string {
	return batteryLabel(arm) + " " + batteryPercent(s.batteryValue(arm))
}

func batteryLabel(arm scouterkit.Arm) string {
	if arm == scouterkit.Right {
		return "R"
	}
	return "L"
}

func (s *screen) batteryValue(arm scouterkit.Arm) *int {
	for _, reading := range s.batteries.arms {
		if reading.arm == arm {
			return reading.percent
		}
	}
	return nil
}

func (s *screen) drawBatteryValue(label string, percent *int, baseline int) {
	x := s.statusX + textInset
	x += s.footerFont.DrawLine(s.bitmap, label+" ", x, baseline, scouterkit.AlignLeft, 255)
	s.drawPercent(x, percent, false, baseline)
}

func (s *screen) drawIconBattery(icon string, percent *int, charging bool, baseline int) {
	x := s.statusX + textInset
	x += s.drawIconAt(icon, x, baseline, 255) + s.footerFont.Measure(" ").Width
	s.drawPercent(x, percent, charging, baseline)
}

// The percentage, then a bolt while charging, or a warning icon when low.
func (s *screen) drawPercent(x int, percent *int, charging bool, baseline int) {
	x += s.footerFont.DrawLine(s.bitmap, batteryPercent(percent), x, baseline, scouterkit.AlignLeft, 255)
	switch {
	case charging && percent != nil:
		s.drawIconAt("charging", x+s.footerFont.Measure(" ").Width, baseline, 255)
	case percent != nil && *percent <= lowBatteryAt:
		s.drawIconAt("warning", x+s.footerFont.Measure(" ").Width, baseline, 255)
	}
}
