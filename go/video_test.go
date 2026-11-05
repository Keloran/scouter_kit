package scouterkit

import "testing"

// Sizes outside 1...640 x 1...480 panic, as a short frame does; a record over
// max bytes leaves the encoder unchanged.
func TestVideoEncoder(t *testing.T) {
	for _, s := range [][2]int{{0, 10}, {641, 10}, {10, 481}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewVideoEncoder(%d, %d) did not panic", s[0], s[1])
				}
			}()
			NewVideoEncoder(s[0], s[1])
		}()
	}
	e := NewVideoEncoder(9, 3)
	defer e.Close()
	pix := make([]byte, 27)
	pix[1], pix[8] = 1, 1
	if e.Encode(pix, false, 1) != nil {
		t.Error("a record over max bytes was made")
	}
	if r := e.Encode(pix, true, 0); len(r) < 2 || r[0] != 3 {
		t.Errorf("first record %x: want the reset and redraw flags", r)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a short frame did not panic")
			}
		}()
		e.Encode(pix[:26], false, 0)
	}()
}
