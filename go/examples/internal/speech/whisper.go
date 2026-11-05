//go:build whisper

package speech

/*
#cgo pkg-config: whisper-static
#include <stdlib.h>
#include <whisper.h>

static void quiet(enum ggml_log_level level, const char *text, void *user) {}
static void sk_whisper_quiet(void) { whisper_log_set(quiet, NULL); }
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// Available reports whether the build has a recogniser.
const Available = true

type whisper struct {
	ctx      *C.struct_whisper_context
	vad      *C.struct_whisper_vad_context
	language *C.char
	threads  int
}

// Open loads a whisper.cpp model.
func Open(m Model) (Recognizer, error) {
	C.sk_whisper_quiet()
	if m.Language == "" {
		m.Language = "en"
	}
	if m.Language != "auto" {
		l := C.CString(m.Language)
		id := C.whisper_lang_id(l)
		C.free(unsafe.Pointer(l))
		if id < 0 {
			return nil, fmt.Errorf("speech: unknown language %q", m.Language)
		}
	}
	path := C.CString(m.Path)
	defer C.free(unsafe.Pointer(path))
	cp := C.whisper_context_default_params()
	cp.use_gpu = false
	cp.flash_attn = false // with it, the cross-attention also reads the encoder's unwritten padding up to 256 when audio_ctx is cut
	ctx := C.whisper_init_from_file_with_params(path, cp)
	if ctx == nil {
		return nil, fmt.Errorf("speech: cannot load the model %s", m.Path)
	}
	if m.Language != "en" && C.whisper_is_multilingual(ctx) == 0 {
		C.whisper_free(ctx)
		return nil, fmt.Errorf("speech: %s is an English-only model (language %q)", m.Path, m.Language)
	}
	w := &whisper{ctx: ctx, language: C.CString(m.Language), threads: max(1, m.Threads)}
	if m.VAD != "" {
		vp := C.whisper_vad_default_context_params()
		vp.use_gpu = false
		path := C.CString(m.VAD)
		w.vad = C.whisper_vad_init_from_file_with_params(path, vp)
		C.free(unsafe.Pointer(path))
		if w.vad == nil {
			w.Close()
			return nil, fmt.Errorf("speech: cannot load the voice activity model %s", m.VAD)
		}
	}
	return w, nil
}

func (w *whisper) Transcribe(pcm []float32, prompt string) (string, error) {
	if w.ctx == nil {
		return "", errors.New("speech: the recogniser is closed")
	}
	if len(pcm) == 0 {
		return "", nil
	}
	p := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	p.n_threads = C.int(w.threads)
	p.no_context = true
	p.no_timestamps = true
	p.single_segment = true
	p.print_progress = false
	p.print_realtime = false
	p.print_timestamps = false
	p.print_special = false
	p.suppress_blank = true
	p.suppress_nst = true
	p.language = w.language
	p.audio_ctx = C.int(min(1500, len(pcm)*50/16000+64))
	if prompt != "" {
		cp := C.CString(prompt)
		defer C.free(unsafe.Pointer(cp))
		p.initial_prompt = cp
	}
	if C.whisper_full(w.ctx, p, (*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm))) != 0 {
		return "", errors.New("speech: whisper_full failed")
	}
	var b strings.Builder
	for i := C.int(0); i < C.whisper_full_n_segments(w.ctx); i++ {
		b.WriteString(C.GoString(C.whisper_full_get_segment_text(w.ctx, i)))
	}
	return strings.Join(unrepeat(strings.Fields(b.String())), " "), nil
}

// Voiced reports whether the voice activity model, if any, finds at least
// 250 ms of speech in the audio.
func (w *whisper) Voiced(pcm []float32) bool {
	if w.vad == nil || len(pcm) == 0 {
		return true
	}
	if !C.whisper_vad_detect_speech(w.vad, (*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm))) {
		return true
	}
	n := int(C.whisper_vad_n_probs(w.vad))
	if n == 0 {
		return false
	}
	speech := 0
	for _, p := range unsafe.Slice(C.whisper_vad_probs(w.vad), n) {
		if p > 0.5 {
			speech++
		}
	}
	return speech*len(pcm)/n >= 4000
}

func (w *whisper) Close() {
	if w.ctx != nil {
		C.whisper_free(w.ctx)
		C.free(unsafe.Pointer(w.language))
		w.ctx = nil
	}
	if w.vad != nil {
		C.whisper_vad_free(w.vad)
		w.vad = nil
	}
}
