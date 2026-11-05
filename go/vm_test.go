//go:build emulator

package scouterkit

import (
	"encoding/binary"
	"testing"
	"time"
)

// A program on both emulated arms: USER0 answers with NOTIFY [box] + 1, USER1
// halts with an error, USER2 with HALT.
func TestProgramRoundTrip(t *testing.T) {
	start(t)
	base, err := Alloc(512)
	if err != nil {
		t.Fatal(err)
	}
	box := base + 508
	p := NewProgramAt(base, 8)
	echo, fail, stop := p.Label(), p.Label(), p.Label()
	p.Bind(echo)
	p.Add(R(1), Word(box/4), p.Const(1))
	p.Notify(R(1), p.Const(0xC0DE))
	p.End()
	p.Bind(fail)
	p.Load(32, R(2), p.Const(0x40000), p.Const(0))
	p.End()
	p.Bind(stop)
	p.Halt()
	p.On(TriggerUser0, echo)
	p.On(TriggerUser1, fail)
	p.On(TriggerUser2, stop)
	p.Changes(box, 4)
	for _, arm := range []Arm{Left, Right} {
		if err := arm.Load(p); err != nil {
			t.Fatal(err)
		}
		if err := arm.Start(base); err != nil {
			t.Fatal(err)
		}
		arm.WriteArena(box, []byte{41, 0, 0, 0})
		sent := NowUS()
		arm.Raise(TriggerUser0)
		e, ok := arm.NextEvent(2 * time.Second)
		if !ok || e.Type != EventNotify || e.A != 42 || e.B != 0xC0DE || e.Trigger != int(TriggerUser0) {
			t.Fatalf("%s arm: %+v %v", arm, e, ok)
		}
		t.Logf("%s arm: RAISE to NOTIFY %.1f ms", arm, float64(e.HostUS-sent)/1000)
		arm.Raise(TriggerUser1)
		e, ok = arm.NextEvent(2 * time.Second)
		if !ok || e.Type != EventHalt || e.Code != 3 || e.Op != 0x20 || e.Detail != 0x40000 {
			t.Fatalf("%s arm: %+v %v", arm, e, ok)
		}
		s, err := arm.VMStatus()
		if err != nil || s.State != 2 || s.HaltCode != 3 || s.Next != e.Address {
			t.Errorf("%s arm: status %+v, %v", arm, s, err)
		}
		if err := arm.Start(base); err != nil {
			t.Fatal(err)
		}
		arm.Raise(TriggerUser2)
		if e, ok = arm.NextEvent(2 * time.Second); !ok || e.Type != EventHalt || e.Code != 0 || e.Op != 0x34 {
			t.Errorf("%s arm: %+v %v", arm, e, ok)
		}
		if _, _, err := arm.ArenaCRC(box, 4); err == nil {
			t.Errorf("%s arm: a CRC of what the program changes", arm)
		}
		if got, want, err := arm.ArenaCRC(p.Here()-uint32(len(p.Code())), uint32(len(p.Code()))); err != nil || got != want {
			t.Errorf("%s arm: code CRC %08x, want %08x, %v", arm, got, want, err)
		}
		b, err := arm.ReadArena(box, 4)
		if err != nil || binary.LittleEndian.Uint32(b) != 41 {
			t.Errorf("%s arm: read back %v, %v", arm, b, err)
		}
		if err := arm.Stop(); err != nil {
			t.Error(err)
		}
	}
	Free(base)
	if got, want, err := Left.ArenaCRC(box, 4); err != nil || got != want {
		t.Errorf("after Free: %08x, %08x, %v", got, want, err)
	}
}

func TestPropsAndInputs(t *testing.T) {
	start(t)
	v, err := Right.Props(PropBattMV, PropBattPct, PropArm, PropGyrX)
	if err != nil || v[0] != 3950 || v[1] != 72 || v[2] != 0 || v[3] != Unknown {
		t.Errorf("props %v, %v", v, err)
	}
	if v, err := Left.Props(PropArm); err != nil || v[0] != 1 {
		t.Errorf("left ARM %v, %v", v, err)
	}
	inForce, err := Left.ReportInputs(0xFFFF)
	if err != nil || inForce != Left.State().Triggers || inForce != 0xF063 {
		t.Errorf("EVENTS: %04x, %v", inForce, err)
	}
	Left.SetEmulatedProperty(PropBattPct, 50)
	e, ok := Left.NextEvent(2 * time.Second)
	if !ok || e.Type != EventInput || e.Trigger != int(TriggerBattery) || e.A != 50 {
		t.Errorf("INPUT %+v %v", e, ok)
	}
	if s := Left.State(); s.Microphone || s.SafeMode || !s.Encrypted {
		t.Errorf("state %+v", s)
	}
}

func TestFontAndBitmapAddresses(t *testing.T) {
	start(t)
	f, err := LoadFont(DepartureMono(), "0123456789:", 2)
	if err != nil {
		t.Fatal(err)
	}
	if g := f.GlyphIndices("09: 5"); len(g) != 5 || g[0] != 0 || g[1] != 9 || g[2] != 63 || g[3] != 62 || g[4] != 5 {
		t.Errorf("glyphs %v", g)
	}
	all, err := LoadFont(DepartureMono(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	replacement := all.GlyphIndices("�")
	if g := all.GlyphIndices("é€…"); len(g) != 3 || g[1] != 814 || g[0] == g[1] || g[1] == g[2] ||
		len(replacement) == 1 && (g[0] == replacement[0] || g[1] == replacement[0] || g[2] == replacement[0]) {
		t.Errorf("glyphs of é€…: %v (U+FFFD %v)", g, replacement)
	}
	b, err := CreateBitmap(160, 32)
	if err != nil {
		t.Fatal(err)
	}
	address, stride := b.Address()
	if stride != 80 || address == 0 || address == f.Address() {
		t.Errorf("bitmap at %d, stride %d; font at %d", address, stride, f.Address())
	}
	blob, err := Left.ReadArena(f.Address(), 16)
	if err != nil || string(blob[:3]) != "SKF" {
		t.Errorf("font blob %q, %v", blob, err)
	}
	if m := Right.Mirror(f.Address(), 16); string(m) != string(blob) {
		t.Errorf("mirror %x, arena %x", m, blob)
	}
	if Left.Mirror(262140, 8) != nil {
		t.Error("a mirror range past the arena")
	}
}

// Host code: NOTIFY, SYS_WRITE, CALL into stored code, Run with a Program's
// code, the arm's clock and a link drop.
func TestHostCode(t *testing.T) {
	start(t)
	base, err := Alloc(256)
	if err != nil {
		t.Fatal(err)
	}
	sub := NewProgramAt(base, 4)
	box := Word((base + 252) / 4)
	at := sub.Here()
	sub.Add(box, box, sub.Const(5))
	sub.Ret()
	sub.Changes(base+252, 4)
	if err := Left.Load(sub); err != nil {
		t.Fatal(err)
	}
	Left.WriteArena(base+252, []byte{10, 0, 0, 0})
	Left.Call(at)
	host := NewProgramAt(base, 0)
	host.Write(base+248, []byte{7, 0, 0, 0})
	host.Notify(Word((base+248)/4), box)
	Left.Run(host.Code())
	e, ok := Left.NextEvent(2 * time.Second)
	if !ok || e.Type != EventNotify || e.A != 7 || e.B != 15 || e.Trigger != 0xFF {
		t.Errorf("NOTIFY after CALL: %+v %v", e, ok)
	}
	Left.Notify(3, 4)
	if e, ok := Left.NextEvent(2 * time.Second); !ok || e.A != 3 || e.B != 4 {
		t.Errorf("NOTIFY: %+v %v", e, ok)
	}
	Right.SysWrite(PropBrightness, 33)
	if _, _, err := Right.ArenaCRC(base, 4); err != nil {
		t.Error(err)
	}
	if v, err := Right.Props(PropBrightness); err != nil || v[0] != 33 {
		t.Errorf("brightness %v, %v", v, err)
	}
	t0 := Right.DeviceTime(0)
	if v, err := Right.Props(PropTimeUS); err != nil || int32(v[0]-t0) < 0 || int32(v[0]-t0) > 200000 {
		t.Errorf("TIME_US %v against %d, %v", v, t0, err)
	}
	Right.Disconnect(500 * time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if Right.State().Connected {
		t.Error("still connected")
	}
	if _, _, err := Right.ArenaCRC(base, 4); err != nil || Right.State().Reconnects != 1 {
		t.Errorf("after the drop: %v, %+v", err, Right.State())
	}
}
