package handover

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const fixtures = "../../../../tests/handover/"

func readFixture(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaCopyMatches(t *testing.T) {
	b, err := os.ReadFile(fixtures + "protocol.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, protocolSchemaJSON) {
		t.Fatal("protocol.schema.json differs from tests/handover/protocol.schema.json: copy it again")
	}
}

type messageCase struct {
	Name    string          `json:"name"`
	Message json.RawMessage `json:"message"`
}

func frameOf(payload []byte) []byte {
	f := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	return append(f, payload...)
}

func TestMessageFixtures(t *testing.T) {
	var f struct {
		Valid, Invalid []messageCase
	}
	readFixture(t, "messages.json", &f)
	if len(f.Valid) < 10 || len(f.Invalid) < 20 {
		t.Fatalf("messages.json: %d valid, %d invalid", len(f.Valid), len(f.Invalid))
	}
	for _, c := range f.Valid {
		m, err := ReadFrame(bytes.NewReader(frameOf(c.Message)))
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		var out bytes.Buffer
		if err := WriteFrame(&out, m); err != nil {
			t.Errorf("%s: write: %v", c.Name, err)
			continue
		}
		var want, got any
		json.Unmarshal(c.Message, &want)
		json.Unmarshal(out.Bytes()[4:], &got)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: round trip\n got  %s\n want %s", c.Name, out.Bytes()[4:], c.Message)
		}
	}
	for _, c := range f.Invalid {
		if _, err := ReadFrame(bytes.NewReader(frameOf(c.Message))); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (%v)", c.Name, err)
		}
	}
}

func TestStreamFixtures(t *testing.T) {
	var f struct {
		Identities struct {
			Sender, Recipient string
			LocalSession      string `json:"local_session"`
		}
		Streams []struct {
			Name    string
			Options struct {
				Retired           []string `json:"retired"`
				Pending           string   `json:"pending"`
				LocalCapabilities []string `json:"local_capabilities"`
			}
			Steps []struct {
				Message json.RawMessage
				Accept  bool
			}
		}
	}
	readFixture(t, "sequences.json", &f)
	if len(f.Streams) < 15 {
		t.Fatalf("sequences.json: %d streams", len(f.Streams))
	}
	id := f.Identities
	for _, c := range f.Streams {
		s := NewStream(id.Sender, id.Recipient, id.LocalSession, c.Options.Retired, c.Options.Pending, c.Options.LocalCapabilities)
		for i, step := range c.Steps {
			m, err := ReadFrame(bytes.NewReader(frameOf(step.Message)))
			if err == nil {
				err = s.Receive(m)
			}
			if step.Accept && err != nil {
				t.Errorf("%s step %d: rejected: %v", c.Name, i, err)
			}
			if !step.Accept && err == nil {
				t.Errorf("%s step %d: accepted", c.Name, i)
			}
		}
	}
}

func validHello(t testing.TB) []byte {
	var f struct{ Valid []messageCase }
	readFixture(t, "messages.json", &f)
	var b bytes.Buffer
	json.Compact(&b, f.Valid[0].Message)
	if !strings.Contains(b.String(), `"seq":1,`) {
		t.Fatal("unexpected hello fixture")
	}
	return b.Bytes()
}

func TestFrameErrors(t *testing.T) {
	hello := validHello(t)
	for name, data := range map[string][]byte{
		"zero length":   {0, 0, 0, 0},
		"oversized":     binary.BigEndian.AppendUint32(nil, MaxFrameBytes+1),
		"duplicate key": frameOf([]byte(strings.Replace(string(hello), `"seq":1`, `"seq":1,"seq":2`, 1))),
		"trailing":      frameOf(append(append([]byte{}, hello...), []byte(" {}")...)),
		"lone surrogate": frameOf([]byte(strings.Replace(string(hello),
			`"cccccccccccccccccccccccccccccccc"`, `"\ud800ccccccccccccccccccccccccccccccc"`, 1))),
		"deep":        frameOf([]byte(strings.Repeat("[", 17) + strings.Repeat("]", 17))),
		"bad utf8":    frameOf([]byte{'"', 0xff, '"'}),
		"float seq":   frameOf([]byte(strings.Replace(string(hello), `"seq":1`, `"seq":1.0`, 1))),
		"huge number": frameOf([]byte(strings.Replace(string(hello), `"epoch":0`, `"epoch":1e400`, 1))),
	} {
		if _, err := ReadFrame(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Cut anywhere: an error, never a message.
	full := frameOf(hello)
	for n := 0; n < len(full); n++ {
		if _, err := ReadFrame(bytes.NewReader(full[:n])); err == nil {
			t.Fatalf("truncated at %d accepted", n)
		} else if n > 0 && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated at %d: %v", n, err)
		}
	}
}

func FuzzDecodeMessage(f *testing.F) {
	var m struct{ Valid, Invalid []messageCase }
	readFixture(f, "messages.json", &m)
	for _, c := range append(m.Valid, m.Invalid...) {
		f.Add([]byte(c.Message))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		msg, err := DecodeMessage(payload)
		if err != nil {
			return
		}
		// Whatever decodes must encode and decode to the same message.
		var out bytes.Buffer
		if err := WriteFrame(&out, msg); err != nil {
			t.Fatalf("decoded but cannot re-encode: %v", err)
		}
		again, err := ReadFrame(&out)
		if err != nil || !reflect.DeepEqual(normal(again), normal(msg)) {
			t.Fatalf("round trip changed the message: %v", err)
		}
	})
}

func normal(m Message) Message {
	var b bytes.Buffer
	json.Compact(&b, m.Body)
	m.Body = b.Bytes()
	return m
}
