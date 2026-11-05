package speech

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
)

// Synth speaks text with espeak-ng (synthetic speech for tests) and returns
// it as 16 kHz mono.
func Synth(text, voice string, wpm int) ([]float32, error) {
	dir, err := os.MkdirTemp("", "speech")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "speech.wav")
	if out, err := exec.Command("espeak-ng", "-v", voice, "-s", fmt.Sprint(wpm), "-w", path, text).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("espeak-ng: %v: %s", err, out)
	}
	return Decode(path)
}

// WriteWAV writes 16 kHz mono samples as a 16-bit WAV file.
func WriteWAV(path string, pcm []float32) error {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(max(-32768, min(32767, math.Round(float64(v)*32768))))))
	}
	return os.WriteFile(path, b, 0o644)
}
