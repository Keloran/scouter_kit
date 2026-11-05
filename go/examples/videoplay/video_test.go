//go:build emulator

package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"scouterkit/examples/internal/demo"
)

// A generated clip (two moving discs on black, a tone) played on the
// emulated arms in every mode, both panels read back after the last frame.
func TestPlay(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	clip := filepath.Join(t.TempDir(), "discs.mp4")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",
		"color=c=black:s=320x240:r=30:d=2,format=gray,geq=lum='255*(lt(hypot(X-160-90*sin(T*3),Y-120),40)+lt(hypot(X-160,Y-120-60*cos(T*2)),25))'",
		"-f", "lavfi", "-i", "sine=f=330:d=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	for _, c := range []config{{levels: 2}, {levels: 16}, {levels: 2, stream: true}, {levels: 2, upload: true}} {
		c.path, c.width, c.height, c.fps, c.threshold, c.crop, c.check = clip, 540, 460, 15, 128, "none", true
		c.sendLead = 300 * time.Millisecond
		if err := run(demo.Flags{}, c); err != nil {
			t.Fatalf("levels %d, stream %v: %v", c.levels, c.stream, err)
		}
	}
}
