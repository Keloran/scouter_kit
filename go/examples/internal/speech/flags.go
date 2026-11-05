package speech

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"time"

	"scouterkit"
)

// Flags are the speech demos' input and recogniser flags.
type Flags struct {
	Model, VAD, Live, Language, Audio string
	Threads, Rate, Codec              int
	Pace, Denoise                     bool
	Step                              time.Duration
}

func (f *Flags) Register(step time.Duration) {
	flag.StringVar(&f.Model, "model", "", "whisper.cpp model, ggml-*.bin (fetch_model.sh; small-q5_1 recommended)")
	flag.StringVar(&f.VAD, "vad", "", "Silero voice activity model, ggml-silero-*.bin (fetch_model.sh): sounds without speech are not recognised")
	flag.StringVar(&f.Live, "live", "", "a streaming model for sherpa-onnx, its directory (fetch_model.sh): words as they are spoken; -model is then not needed")
	flag.StringVar(&f.Language, "lang", "en", `spoken language: "en", "de", "pl", ... or "auto"`)
	flag.IntVar(&f.Threads, "threads", 8, "recogniser threads")
	flag.StringVar(&f.Audio, "audio", "", "speech from this audio file (anything ffmpeg reads) in place of the glasses' microphone")
	flag.BoolVar(&f.Pace, "pace", true, "with -audio: in real time")
	flag.IntVar(&f.Codec, "codec", 0, "with -audio: through the microphone link's LC3 codec at 32 or 16 kbps (0: no)")
	flag.IntVar(&f.Rate, "rate", 32, "the microphone's LC3 bitrate, 32 or 16 kbps")
	flag.BoolVar(&f.Denoise, "denoise", true, "noise suppression for the segmenter and the recogniser")
	flag.DurationVar(&f.Step, "step", step, "new speech between partial results (0: final results only)")
}

// Check validates the flags.
func (f Flags) Check() error {
	switch {
	case f.Rate != 32 && f.Rate != 16:
		return fmt.Errorf("-rate %d: 32 or 16", f.Rate)
	case f.Codec != 0 && f.Codec != 32 && f.Codec != 16:
		return fmt.Errorf("-codec %d: 32, 16 or 0", f.Codec)
	case f.Threads < 1:
		return fmt.Errorf("-threads %d: at least 1", f.Threads)
	}
	return nil
}

// Open opens the recogniser.
func (f Flags) Open() (Recognizer, error) {
	if f.Model == "" {
		return nil, errors.New("-live DIR (a streaming model, fetch_model.sh DIR fastconformer-en-80ms) or -model FILE (a whisper.cpp model)")
	}
	return Open(Model{Path: f.Model, VAD: f.VAD, Language: f.Language, Threads: f.Threads})
}

// Recognition opens the recogniser of the flags: the streaming one with
// -live, else whisper.cpp with local agreement (rec, if not nil, instead of
// -model). close frees it.
func (f Flags) Recognition(rec Recognizer) (r Recognition, close func(), err error) {
	if f.Live != "" {
		l, err := OpenLive(f.Live, min(f.Threads, 4))
		if err != nil {
			return nil, nil, err
		}
		ls := &LiveStream{Rec: l, Seg: NewSegmenter()}
		if f.Denoise {
			ls.Denoiser = NewDenoiser()
		}
		return ls, l.Close, nil
	}
	close = func() {}
	if rec == nil {
		if rec, err = f.Open(); err != nil {
			return nil, nil, err
		}
		close = rec.Close
	}
	return AgreeStream{f.Stream(rec)}, close, nil
}

// Stream is a stream for the flags.
func (f Flags) Stream(rec Recognizer) *Stream {
	s := &Stream{Rec: rec, Seg: NewSegmenter(), Step: int(f.Step.Seconds() * scouterkit.MicSampleRate)}
	if f.Denoise {
		s.Denoiser, s.DenoiseSpeech = NewDenoiser(), true
	}
	return s
}

// Mic reports whether the input is the glasses' microphone.
func (f Flags) Mic() bool { return f.Audio == "" }

// Input starts the speech input: the file of -audio, or the right arm's
// microphone, which is on (its indicator lit) until stop or the link drops.
// The frames' channel closes at the end of the file, at stop, or when the
// microphone stops by itself (scouterkit.Right.MicState().Stopped says why).
func (f Flags) Input() (frames <-chan scouterkit.MicFrame, stop func(), err error) {
	if !f.Mic() {
		pcm, err := Decode(f.Audio)
		if err != nil {
			return nil, nil, err
		}
		if f.Codec != 0 {
			if pcm, err = Codec(pcm, f.Codec); err != nil {
				return nil, nil, err
			}
		}
		done := make(chan struct{})
		return Play(pcm, f.Pace, done), func() { close(done) }, nil
	}
	arm := scouterkit.Right
	switch s := arm.State(); {
	case !s.Present:
		return nil, nil, errors.New("the microphone is on the right arm, which is not connected (-audio FILE: speech from a file)")
	case !s.Microphone:
		return nil, nil, errors.New("the right arm's firmware has no microphone capture")
	}
	rate := scouterkit.Mic32kbps
	if f.Rate == 16 {
		rate = scouterkit.Mic16kbps
	}
	ch, err := arm.MicStart(rate)
	if err != nil {
		return nil, nil, err
	}
	return ch, func() {
		if err := arm.MicStop(); err != nil {
			fmt.Println("right arm: MIC 0:", err)
		}
	}, nil
}

// DBFS is an RMS in dB of full scale.
func DBFS(rms float64) float64 { return 20 * math.Log10(max(rms, 1e-6)) }
