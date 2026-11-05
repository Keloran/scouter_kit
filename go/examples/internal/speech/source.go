package speech

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"time"

	"scouterkit"
)

// Decode reads an audio file, anything ffmpeg reads, as 16 kHz mono
// samples in [-1, 1].
func Decode(path string) ([]float32, error) {
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", path, "-f", "s16le", "-ac", "1", "-ar", "16000", "-")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("ffmpeg %s: %s", path, ee.Stderr)
		}
		return nil, err
	}
	pcm := make([]float32, len(out)/2)
	for i := range pcm {
		pcm[i] = float32(int16(binary.LittleEndian.Uint16(out[2*i:]))) / 32768
	}
	return pcm, nil
}

// File plays an audio file, anything ffmpeg reads, in place of the glasses'
// microphone (see Play).
func File(path string, pace bool, done <-chan struct{}) (<-chan scouterkit.MicFrame, error) {
	pcm, err := Decode(path)
	if err != nil {
		return nil, err
	}
	return Play(pcm, pace, done), nil
}

// Play gives samples as the microphone's frames: 10 ms of 16 kHz mono each,
// five at a time every 50 ms as the arm sends them (all at once without
// pace). The channel closes at the end or when done closes.
func Play(pcm []float32, pace bool, done <-chan struct{}) <-chan scouterkit.MicFrame {
	frames := make(chan scouterkit.MicFrame, 10)
	go func() {
		defer close(frames)
		t0 := time.Now()
		for k := 0; (k+1)*scouterkit.MicFrameSamples <= len(pcm); k++ {
			if pace && k%5 == 0 {
				time.Sleep(time.Until(t0.Add(time.Duration(k+5) * 10 * time.Millisecond)))
			}
			f := scouterkit.MicFrame{Counter: uint32(k), DeviceUS: uint32(k * 10000), HostUS: scouterkit.NowUS()}
			for i, v := range pcm[k*scouterkit.MicFrameSamples : (k+1)*scouterkit.MicFrameSamples] {
				f.PCM[i] = int16(max(-32768, min(32767, math.Round(float64(v)*32768))))
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()
	return frames
}
