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

func (s *screen) drawNavigationRow(index, total int) {
	baseline := s.footerBaseline
	footerTop := baseline - s.footerFont.Ascent - 5
	footerBottom := baseline + s.footerFont.Descent
	s.bitmap.Fill(image.Rect(s.w/3, footerTop, s.w/3+1, footerBottom), 153)
	left := footerRow{screen: s, x: textInset, baseline: baseline, limit: s.w / 3}
	left.text(fmt.Sprintf("Page %d/%d", index+1, total))
	middle := footerRow{screen: s, x: s.w/3 + textInset, baseline: baseline, limit: s.w - textInset}
	statusWidth := middle.limit - middle.x
	label := s.status.line(time.Now(), statusWidth, func(text string) int { return s.footerFont.Measure(text).Width })
	middleWidth := s.footerFont.Measure(label).Width
	if middleWidth < middle.limit-middle.x {
		middle.x += (middle.limit - middle.x - middleWidth) / 2
	}
	middle.text(label)
}

func (s *screen) drawStatusPanel(paused bool) {
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
	line := func(text string, baseline int) {
		text = fitLine(text, s.statusW-2*textInset, func(v string) int { return s.footerFont.Measure(v).Width })
		s.footerFont.DrawLine(s.bitmap, text, s.statusX+textInset, baseline, scouterkit.AlignLeft, 255)
	}
	line(s.batteryFor(scouterkit.Left), iconBaseline+s.footerFont.LineSpacing)
	line(s.batteryFor(scouterkit.Right), rightBaseline)

	computerW, computerH := s.iconSize("computer")
	middleHeight := panelLower - panelMid
	middleContentHeight := computerH + s.footerFont.LineSpacing
	middleTop := panelMid + (middleHeight-middleContentHeight)/2
	computerBaseline := middleTop + computerH + 2
	computerX := s.statusX + (s.statusW-computerW)/2
	s.drawIconAt("computer", computerX, computerBaseline, 255)
	line(batteryPercent(s.batteries.host), computerBaseline+s.footerFont.LineSpacing)

	modeIcon := "refresh"
	if paused {
		modeIcon = "hand"
	}
	bottomHeight := footerTop - panelLower
	modeW, modeH := s.iconSize(modeIcon)
	modeX := s.statusX + (s.statusW-modeW)/2
	modeTop := panelLower + (bottomHeight-modeH)/2
	modeBaseline := modeTop + modeH - min(s.footerFont.Descent, s.footerFont.LineSpacing-s.footerFont.Ascent)
	s.drawIconAt(modeIcon, modeX, modeBaseline, 255)
}

func (s *screen) batteryFor(arm scouterkit.Arm) string {
	for _, reading := range s.batteries.arms {
		if reading.arm == arm {
			label := "L"
			if arm == scouterkit.Right {
				label = "R"
			}
			return label + " " + batteryPercent(reading.percent)
		}
	}
	if arm == scouterkit.Right {
		return "R --%"
	}
	return "L --%"
}
