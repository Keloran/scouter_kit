// Command videoplay plays a video file on both eyes. ffmpeg decodes it to
// gray frames that fit the columns both eyes see. In black and white (the
// default) each frame goes as a VIDEO record (codec.go) that the arms decode
// and draw at its time; with -levels 16 (or -upload) it goes into one bitmap
// on the glasses (Flush uploads only the pixels that changed) and is shown
// on both eyes at once. A frame the link cannot keep up with is skipped, so
// the picture keeps to the clock; -audio plays the sound on the PC along
// with it (ffplay).
//
//	go run ./examples/videoplay [flags] FILE
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/picture"
)

type config struct {
	path          string
	width, height int
	fps           float64
	levels        int
	threshold     int
	start, length time.Duration
	crop          string
	audio         bool
	audioDelay    time.Duration
	check         bool
	stream        bool
	upload        bool
	sendLead      time.Duration
	save          string
	telemetry     time.Duration
	brightness    int
}

func main() {
	var f demo.Flags
	var c config
	f.Register()
	flag.IntVar(&c.width, "width", 540, "the largest picture width (at most the columns both eyes see)")
	flag.IntVar(&c.height, "height", 460, "the largest picture height")
	flag.Float64Var(&c.fps, "fps", 15, "frames per second")
	flag.IntVar(&c.levels, "levels", 2, "gray levels: 2 (black and white, the least data) or 16")
	flag.IntVar(&c.threshold, "threshold", 128, "with -levels 2: the luma from which a pixel is lit")
	flag.DurationVar(&c.start, "start", 0, "where in the file to begin")
	flag.DurationVar(&c.length, "for", 0, "how much of it to play (0: to the end)")
	flag.StringVar(&c.crop, "crop", "auto", `"auto" (cut black borders), "none" or ffmpeg's W:H:X:Y`)
	flag.BoolVar(&c.audio, "audio", false, "play the sound on the PC (ffplay)")
	flag.DurationVar(&c.audioDelay, "audio-delay", 0, "start the sound this much after the first frame is shown")
	flag.BoolVar(&c.check, "check", false, "read both panels back after the last frame")
	flag.BoolVar(&c.stream, "stream", false, "with -levels 2: send each frame as row runs that stored code composes (g1v0 0.5.40)")
	flag.BoolVar(&c.upload, "upload", false, "with -levels 2: upload each frame's changed pixels into a 4-bpp bitmap (firmware without VIDEO)")
	flag.DurationVar(&c.sendLead, "lead", 300*time.Millisecond, "with -levels 2: send each frame this long before it is due")
	flag.StringVar(&c.save, "save", "", "write the display read-back of the last frame as a PNG")
	flag.IntVar(&c.brightness, "brightness", -1, "set both panels' brightness 0...42 first (-1: leave it)")
	flag.DurationVar(&c.telemetry, "telemetry", 0, "with VIDEO records: print each arm's battery voltage and current, its panel state and reboots, and the frame's lit share this often (0: never)")
	demo.Usage("videoplay [flags] FILE")
	flag.Parse()
	if flag.NArg() != 1 || (c.levels != 2 && c.levels != 16) || c.fps <= 0 || c.fps > 60 {
		flag.Usage()
		os.Exit(2)
	}
	c.path = flag.Arg(0)
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "videoplay:", err)
		os.Exit(1)
	}
}

func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

// probe returns the picture's size as shown (the sample aspect ratio
// applied) after the crop, and the crop for ffmpeg ("" for none).
func probe(c config) (w, h float64, crop string, err error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,sample_aspect_ratio", "-of", "csv=p=0", c.path).Output()
	if err != nil {
		return 0, 0, "", fmt.Errorf("ffprobe %s: %w", c.path, err)
	}
	f := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(f) < 2 {
		return 0, 0, "", fmt.Errorf("%s: no video stream", c.path)
	}
	vw, _ := strconv.Atoi(f[0])
	vh, _ := strconv.Atoi(f[1])
	sar := 1.0
	if len(f) > 2 {
		if n, d, ok := strings.Cut(f[2], ":"); ok {
			a, _ := strconv.ParseFloat(n, 64)
			b, _ := strconv.ParseFloat(d, 64)
			if a > 0 && b > 0 {
				sar = a / b
			}
		}
	}
	cw, ch := vw, vh
	switch c.crop {
	case "none":
	case "auto":
		crop, cw, ch = detectCrop(c, vw, vh)
	default:
		crop = c.crop
		p := strings.Split(crop, ":")
		if len(p) != 4 {
			return 0, 0, "", fmt.Errorf("-crop %q: want W:H:X:Y", crop)
		}
		cw, _ = strconv.Atoi(p[0])
		ch, _ = strconv.Atoi(p[1])
	}
	if vw <= 0 || vh <= 0 || cw <= 0 || ch <= 0 {
		return 0, 0, "", fmt.Errorf("%s: picture size %dx%d, crop %dx%d", c.path, vw, vh, cw, ch)
	}
	return float64(cw) * sar, float64(ch), crop, nil
}

var cropLine = regexp.MustCompile(`crop=(\d+):(\d+):(\d+):(\d+)`)

// detectCrop runs ffmpeg's cropdetect over the key frames of the part
// played: the box that holds everything not black in them.
func detectCrop(c config, vw, vh int) (string, int, int) {
	args := []string{"-nostdin", "-hide_banner", "-skip_frame", "nokey", "-ss", secs(c.start), "-i", c.path}
	if c.length > 0 {
		args = append(args, "-t", secs(c.length))
	}
	args = append(args, "-an", "-vf", "cropdetect=limit=24:round=2:reset=0", "-f", "null", "-")
	out, _ := exec.Command("ffmpeg", args...).CombinedOutput()
	m := cropLine.FindAllStringSubmatch(string(out), -1)
	if len(m) == 0 {
		return "", vw, vh
	}
	last := m[len(m)-1]
	w, _ := strconv.Atoi(last[1])
	h, _ := strconv.Atoi(last[2])
	if w <= 0 || h <= 0 || (w == vw && h == vh) {
		return "", vw, vh
	}
	return strings.Join(last[1:], ":"), w, h
}

func fit(w, h float64, bw, bh int) (int, int) {
	s := math.Min(float64(bw)/w, float64(bh)/h)
	return max(2, int(w*s)&^1), max(2, int(h*s)&^1)
}

func quantize(dst *image.Gray, src []byte, levels, threshold int) {
	for i, v := range src {
		switch {
		case levels == 16:
			dst.Pix[i] = uint8((int(v)*15+127)/255) * 17
		case int(v) >= threshold:
			dst.Pix[i] = 255
		default:
			dst.Pix[i] = 0
		}
	}
}

func sleepUntil(t time.Time) {
	if d := time.Until(t); d > 0 {
		time.Sleep(d)
	}
}

func run(f demo.Flags, c config) error {
	vw, vh, crop, err := probe(c)
	if err != nil {
		return err
	}
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	if c.brightness >= 0 {
		for _, arm := range arms {
			if err := arm.SetBrightness(c.brightness); err != nil {
				return fmt.Errorf("%s arm: %w", arm, err)
			}
		}
	}
	_, dh := scouterkit.Size()
	dx := cfg.DX
	if dx < 0 {
		dx = -dx
	}
	both := scouterkit.PanelWidth - dx
	w, h := fit(vw, vh, min(c.width, both), min(c.height, dh))
	x, y := dx+(both-w)/2, (dh-h)/2

	vf := []string{}
	if crop != "" {
		vf = append(vf, "crop="+crop)
	}
	vf = append(vf, fmt.Sprintf("fps=%g", c.fps), fmt.Sprintf("scale=%d:%d:flags=area", w, h), "format=gray")
	args := []string{"-nostdin", "-loglevel", "error"}
	if c.start > 0 {
		args = append(args, "-ss", secs(c.start))
	}
	args = append(args, "-i", c.path)
	if c.length > 0 {
		args = append(args, "-t", secs(c.length))
	}
	args = append(args, "-an", "-vf", strings.Join(vf, ","), "-f", "rawvideo", "-pix_fmt", "gray", "-")
	dec := exec.Command("ffmpeg", args...)
	dec.Stderr = os.Stderr
	out, err := dec.StdoutPipe()
	if err != nil {
		return err
	}
	if err := dec.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	defer dec.Process.Kill()
	r := bufio.NewReaderSize(out, 4*w*h)
	if c.levels == 2 && !c.upload {
		mode := "VIDEO records"
		if c.stream {
			mode = "streamed"
		}
		fmt.Printf("%s: %dx%d at (%d, %d), %g frames/s, %s\n", c.path, w, h, x, y, c.fps, mode)
		scouterkit.Clear()
		scouterkit.Flush()
		scouterkit.Flush()
		if c.stream {
			return streamPlay(cfg, arms, c, w, h, x, y, r)
		}
		return codecPlay(cfg, arms, c, w, h, x, y, r)
	}

	bm, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return err
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	frame := make([]byte, w*h)
	period := time.Duration(float64(time.Second) / c.fps)
	fmt.Printf("%s: %dx%d at (%d, %d), %g frames/s, %d levels\n", c.path, w, h, x, y, c.fps, c.levels)

	var audio *exec.Cmd
	var t0, audioAt time.Time
	packets := map[scouterkit.Arm]uint32{}
	stop := demo.Interrupted()
	shown, skipped, interrupted := 0, 0, false
	for k := 0; ; k++ {
		if _, err := io.ReadFull(r, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		select {
		case <-stop:
			interrupted = true
		default:
		}
		if interrupted {
			break
		}
		if k > 0 {
			due := t0.Add(time.Duration(k) * period)
			if c.audio && audio == nil && !due.Before(audioAt) {
				sleepUntil(audioAt)
				audio = startAudio(c)
			}
			if time.Since(due) > period/2 {
				skipped++
				continue
			}
			sleepUntil(due)
		}
		quantize(img, frame, c.levels, c.threshold)
		bm.Write(img, 0, 0)
		scouterkit.Draw(bm, x, y)
		scouterkit.Flush()
		shown++
		if k == 0 {
			scouterkit.Flush() // returns once the first frame is shown
			t0 = time.Now()
			audioAt = t0.Add(c.audioDelay)
			for _, arm := range arms {
				packets[arm] = arm.State().Packets
			}
		}
	}
	elapsed := time.Since(t0)
	scouterkit.Flush()
	if audio != nil {
		if interrupted {
			audio.Process.Kill()
		}
		audio.Wait()
	}
	fmt.Printf("%d frames shown, %d skipped in %v (%.1f frames/s shown)", shown, skipped,
		elapsed.Round(time.Millisecond), float64(shown-1)/elapsed.Seconds())
	for _, arm := range arms {
		fmt.Printf("; %s %.0f packets/s", arm, float64(arm.State().Packets-packets[arm])/elapsed.Seconds())
	}
	fmt.Println()

	failed := false
	if c.check {
		for _, arm := range arms {
			got, want, err := arm.PanelCRC(0, scouterkit.PanelHeight)
			switch {
			case err != nil:
				fmt.Printf("%s panel: %v\n", arm, err)
				failed = true
			case got != want:
				fmt.Printf("%s panel: read back %08x, expected %08x\n", arm, got, want)
				failed = true
			default:
				fmt.Printf("%s panel: read back %08x, as expected\n", arm, got)
			}
		}
	}
	if c.save != "" {
		if err := picture.SavePNG(c.save, scouterkit.Display()); err != nil {
			return err
		}
	}
	scouterkit.Clear()
	scouterkit.Flush()
	if failed {
		return errors.New("a panel did not read back as expected")
	}
	return nil
}

func startAudio(c config) *exec.Cmd {
	args := []string{"-nodisp", "-autoexit", "-loglevel", "error"}
	if c.start > 0 {
		args = append(args, "-ss", secs(c.start))
	}
	if c.length > 0 {
		args = append(args, "-t", secs(c.length))
	}
	cmd := exec.Command("ffplay", append(args, c.path)...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "videoplay: ffplay:", err)
		return nil
	}
	return cmd
}
