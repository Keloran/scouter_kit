package demo

import (
	"testing"

	"scouterkit"
)

func TestBefore24(t *testing.T) {
	for _, c := range [][3]uint32{{0x12345678, 0x345670, 0x12345670}, {0x01000005, 0xFFFFF0, 0x00FFFFF0}, {5, 5, 5}} {
		if got := Before24(scouterkit.Event{DeviceUS: c[0]}, c[1]); got != c[2] {
			t.Errorf("Before24(%x, %x) = %x, want %x", c[0], c[1], got, c[2])
		}
	}
}
