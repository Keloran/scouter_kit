package speech

import "testing"

// The same utterance gives the same text alone and after decodes of other
// lengths (the cut encoder context once let the padding of earlier decodes
// in).
func TestRecognizerRepeats(t *testing.T) {
	r := openModel(t, "en")
	a := synth(t, "The meeting is over. The mountain path was steep and narrow, but the view from the top was worth it.", "en-us", 160)
	b := synth(t, "Please put the blue folder on the shelf next to the window before you leave.", "en-us", 160)
	alone, err := r.Transcribe(a, "")
	if err != nil {
		t.Fatal(err)
	}
	for n := 8000; n < len(b); n += 4000 {
		if _, err := r.Transcribe(b[:n], ""); err != nil {
			t.Fatal(err)
		}
	}
	for n := 8000; n < len(a); n += 8000 {
		if _, err := r.Transcribe(a[:n], ""); err != nil {
			t.Fatal(err)
		}
	}
	if again, _ := r.Transcribe(a, ""); again != alone {
		t.Fatalf("alone %q, after other decodes %q", alone, again)
	}
}

// The sentences twice in one process, the second time after decodes of
// many other lengths: the same texts.
func TestRecognizerRepeatsAll(t *testing.T) {
	r := openModel(t, "en")
	var pcm [][]float32
	for _, s := range sentences {
		pcm = append(pcm, synth(t, s, "en-us+f3", 150))
	}
	var first []string
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			for _, p := range pcm[:4] {
				for n := 4000; n < len(p); n += 2400 {
					if _, err := r.Transcribe(p[:n], ""); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		for i, p := range pcm {
			text, err := r.Transcribe(p, "")
			if err != nil {
				t.Fatal(err)
			}
			if pass == 0 {
				first = append(first, text)
			} else if text != first[i] {
				t.Errorf("sentence %d: %q, then %q", i, first[i], text)
			}
		}
	}
}
