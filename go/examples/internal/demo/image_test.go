package demo

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestPackBits(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	cases := [][]byte{{}, {5}, {5, 5}, {5, 5, 5}, bytes.Repeat([]byte{9}, 128), bytes.Repeat([]byte{9}, 129),
		bytes.Repeat([]byte{9}, 300)}
	lit := make([]byte, 300)
	for i := range lit {
		lit[i] = byte(i)
	}
	cases = append(cases, lit)
	for i := 0; i < 300; i++ {
		b := make([]byte, rng.Intn(2000))
		for k := 0; k < len(b); {
			n := 1 + rng.Intn(10)
			if rng.Intn(3) == 0 {
				n = 1 + rng.Intn(300)
			}
			v := byte(rng.Intn(4))
			for ; n > 0 && k < len(b); n-- {
				b[k] = v
				k++
			}
		}
		cases = append(cases, b)
	}
	for i, b := range cases {
		p := PackBits(b)
		got, ok := Unpack(p, len(b))
		if !ok || !bytes.Equal(got, b) {
			t.Fatalf("case %d (%d bytes): does not round-trip", i, len(b))
		}
		if len(p) > len(b)+(len(b)+127)/128 {
			t.Fatalf("case %d: %d bytes packed to %d", i, len(b), len(p))
		}
	}
	if _, ok := Unpack([]byte{2, 1, 2}, 3); ok {
		t.Error("a cut-off literal unpacks")
	}
	if _, ok := Unpack([]byte{0xFE, 1}, 2); ok {
		t.Error("a run longer than the output unpacks")
	}
}

func TestRows(t *testing.T) {
	m := NewImage(3, 2)
	m.Set(0, 0, 1)
	m.Set(1, 0, 2)
	m.Set(2, 0, 3)
	m.Set(2, 1, 15)
	if got := m.Rows(0, 2); !bytes.Equal(got, []byte{0x12, 0x30, 0x00, 0xF0}) {
		t.Errorf("rows % x", got)
	}
}
