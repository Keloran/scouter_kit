//go:build sherpa

package speech

/*
#cgo pkg-config: sherpa-onnx-c
#include <stdlib.h>
#include <string.h>
#include <sherpa-onnx/c-api/c-api.h>
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// Streaming reports whether the build has a streaming recogniser.
const Streaming = true

type sherpa struct {
	rec    *C.SherpaOnnxOnlineRecognizer
	stream *C.SherpaOnnxOnlineStream
	strs   []*C.char
}

// OpenLive loads a streaming transducer for sherpa-onnx from a directory
// with encoder, decoder and joiner (*.onnx, int8 preferred) and tokens.txt.
func OpenLive(dir string, threads int) (Live, error) {
	find := func(part string) string {
		for _, pat := range []string{part + "*.int8.onnx", part + "*.onnx"} {
			if m, _ := filepath.Glob(filepath.Join(dir, pat)); len(m) > 0 {
				return m[0]
			}
		}
		return ""
	}
	enc, dec, join, tokens := find("encoder"), find("decoder"), find("joiner"), filepath.Join(dir, "tokens.txt")
	if enc == "" || dec == "" || join == "" {
		return nil, fmt.Errorf("speech: %s holds no encoder, decoder and joiner", dir)
	}
	if _, err := os.Stat(tokens); err != nil {
		return nil, fmt.Errorf("speech: %w", err)
	}
	s := &sherpa{}
	str := func(v string) *C.char {
		p := C.CString(v)
		s.strs = append(s.strs, p)
		return p
	}
	var cfg C.SherpaOnnxOnlineRecognizerConfig
	C.memset(unsafe.Pointer(&cfg), 0, C.sizeof_SherpaOnnxOnlineRecognizerConfig)
	cfg.feat_config.sample_rate = 16000
	cfg.feat_config.feature_dim = 80
	cfg.model_config.transducer.encoder = str(enc)
	cfg.model_config.transducer.decoder = str(dec)
	cfg.model_config.transducer.joiner = str(join)
	cfg.model_config.tokens = str(tokens)
	cfg.model_config.num_threads = C.int32_t(max(1, threads))
	cfg.model_config.provider = str("cpu")
	cfg.decoding_method = str("greedy_search")
	cfg.enable_endpoint = 1
	cfg.rule1_min_trailing_silence = 2.4
	cfg.rule2_min_trailing_silence = 0.8
	cfg.rule3_min_utterance_length = 20
	s.rec = C.SherpaOnnxCreateOnlineRecognizer(&cfg)
	if s.rec == nil {
		s.free()
		return nil, fmt.Errorf("speech: sherpa-onnx cannot load the model in %s", dir)
	}
	s.stream = C.SherpaOnnxCreateOnlineStream(s.rec)
	return s, nil
}

func (s *sherpa) Accept(pcm []float32) (string, bool) {
	if s.rec == nil {
		return "", false
	}
	if len(pcm) > 0 {
		C.SherpaOnnxOnlineStreamAcceptWaveform(s.stream, 16000, (*C.float)(unsafe.Pointer(&pcm[0])), C.int32_t(len(pcm)))
	}
	for C.SherpaOnnxIsOnlineStreamReady(s.rec, s.stream) != 0 {
		C.SherpaOnnxDecodeOnlineStream(s.rec, s.stream)
	}
	r := C.SherpaOnnxGetOnlineStreamResult(s.rec, s.stream)
	text := ""
	if r != nil {
		text = strings.TrimSpace(C.GoString(r.text))
		C.SherpaOnnxDestroyOnlineRecognizerResult(r)
	}
	return text, C.SherpaOnnxOnlineStreamIsEndpoint(s.rec, s.stream) != 0
}

func (s *sherpa) Reset() {
	if s.rec != nil {
		C.SherpaOnnxOnlineStreamReset(s.rec, s.stream)
	}
}

func (s *sherpa) Finish() string {
	if s.rec == nil {
		return ""
	}
	C.SherpaOnnxOnlineStreamInputFinished(s.stream)
	text, _ := s.Accept(nil)
	return text
}

func (s *sherpa) Close() {
	if s.rec != nil {
		C.SherpaOnnxDestroyOnlineStream(s.stream)
		C.SherpaOnnxDestroyOnlineRecognizer(s.rec)
		s.rec = nil
	}
	s.free()
}

func (s *sherpa) free() {
	for _, p := range s.strs {
		C.free(unsafe.Pointer(p))
	}
	s.strs = nil
}
