package speech

import (
	"math/rand"
	"testing"
	"time"
)

// What the voice activity model costs: noise is rejected without decoding,
// so the time is the model's alone.
func TestVoiceCost(t *testing.T) {
	if *vadFlag == "" {
		t.Skip("needs -args -vad ggml-silero-*.bin")
	}
	r := openModel(t, "en")
	rng := rand.New(rand.NewSource(5))
	for _, sec := range []int{2, 10} {
		pcm := make([]float32, sec*16000)
		for i := range pcm {
			pcm[i] = float32(0.05 * rng.NormFloat64())
		}
		t0 := time.Now()
		if r.(Voiced).Voiced(pcm) {
			t.Fatal("noise found voiced")
		}
		t.Logf("%d s of noise rejected in %v", sec, time.Since(t0).Round(time.Millisecond))
	}
}
