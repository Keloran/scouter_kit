package scouterkit

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// VM.md §9.1: word 32 + j holds the j-th value.
var specPool = []uint32{0, 1, 2, 3, 4, 5, 8, 10, 12, 15, 17, 20, 24, 32, 34, 40, 60, 80, 100, 160, 240, 256, 560,
	1440, 16384, 153600, 60000000}

const specClock = "10 ff 44 00 42 00 10 ff 45 00 43 00 30 2b c0 00 11 ff f0 44 00 44 00 21 00 15 ff f0 44 00 44 00 37 00 " +
	"11 ff f0 45 00 45 00 3a 00 14 4f f0 44 00 30 00 15 5f f0 44 00 30 00 14 64 f0 27 00 26 ff 60 35 00 20 00 " +
	"15 64 f0 27 00 26 ff 60 35 00 21 00 14 65 f0 27 00 26 ff 60 35 00 23 00 15 65 f0 27 00 26 ff 60 35 00 24 00 " +
	"50 ff ff ff f0 38 00 31 00 20 00 20 00 33 00 2d 00 20 00 52 ff ff ff ff f0 39 00 38 00 31 00 24 00 2c 00 " +
	"22 00 29 00 35 00 25 00 53 ff ff ff ff 38 00 31 00 20 00 20 00 34 00 26 00 33 00 2d 00 45 ff 2e 00 45 00 33"

const specBattery = "44 0f 2a 00 37 f0 70 c0 00 32 00 10 1f 23 00 12 11 f0 21 00 15 20 f0 27 00 26 f1 20 35 00 14 00 f0 27 00 " +
	"36 0f 0f c0 00 20 00 11 31 f0 35 00 12 4f 10 24 00 50 ff ff ff f0 38 00 2f 00 20 00 20 00 31 00 2c 00 20 00 " +
	"52 ff ff ff f3 40 39 00 38 00 2f 00 22 00 2b 00 22 00 29 00 53 ff ff ff ff 38 00 2f 00 20 00 20 00 36 00 " +
	"26 00 31 00 2c 00 33"

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func specLayout() *Program {
	p := NewProgram(0x40, 0x80, 32, 0xC000)
	for _, v := range specPool {
		p.Const(v)
	}
	return p
}

// specClockProgram is VM.md §9.2: HH:MM in a 160 x 32 band at panel (240, 8).
func specClockProgram() *Program {
	p := specLayout()
	c := p.Const
	const m, n, mNew, nNew Word = 68, 69, 66, 67
	sync, tick, show := p.Label(), p.Label(), p.Label()
	p.Bind(sync)
	p.Mov(m, mNew)
	p.Mov(n, nNew)
	p.Jmp(show)
	p.Bind(tick)
	p.Add(m, m, c(1))
	p.ModU(m, m, c(1440))
	p.Add(n, n, c(60000000))
	p.Bind(show)
	p.DivU(R(4), m, c(60))
	p.ModU(R(5), m, c(60))
	for i, digit := range []struct {
		div   bool
		value Word
	}{{true, R(4)}, {false, R(4)}, {true, R(5)}, {false, R(5)}} {
		if digit.div {
			p.DivU(R(6), digit.value, c(10))
		} else {
			p.ModU(R(6), digit.value, c(10))
		}
		p.Store(8, c(256), c([]uint32{0, 1, 3, 4}[i]), R(6))
	}
	p.Fill(c(16384), c(80), c(0), c(0), c(160), c(32), c(0))
	p.Glyphs(c(153600), c(16384), c(80), c(4), c(24), c(2), c(15), c(256), c(5))
	p.Draw(c(16384), c(80), c(0), c(0), c(240), c(8), c(160), c(32))
	p.SysWrite(c(34), n)
	p.End()
	p.On(TriggerStart, sync)
	p.On(TriggerTimer, tick)
	p.On(TriggerUser0, sync)
	return p
}

// specBatteryProgram is VM.md §9.3: the percentage in an 80 x 24 band at (560, 8).
func specBatteryProgram() *Program {
	p := specLayout()
	c := p.Const
	entry, next, done := p.Label(), p.Label(), p.Label()
	p.Bind(entry)
	p.SysRead(R(0), c(17))
	p.BltU(c(100), R(0), done)
	p.Mov(R(1), c(3))
	p.Bind(next)
	p.Sub(R(1), R(1), c(1))
	p.ModU(R(2), R(0), c(10))
	p.Store(8, c(256), R(1), R(2))
	p.DivU(R(0), R(0), c(10))
	p.Bne(R(0), c(0), next)
	p.Add(R(3), R(1), c(256))
	p.Sub(R(4), c(4), R(1))
	p.Fill(c(16384), c(40), c(0), c(0), c(80), c(24), c(0))
	p.Glyphs(c(153600), c(16384), c(40), c(2), c(20), c(2), c(15), R(3), R(4))
	p.Draw(c(16384), c(40), c(0), c(0), c(560), c(8), c(80), c(24))
	p.Bind(done)
	p.End()
	p.On(TriggerStart, entry)
	p.On(TriggerBattery, entry)
	return p
}

func TestProgramSpecBytes(t *testing.T) {
	clock := specClockProgram()
	if err := clock.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := clock.Code(), unhex(t, specClock); !bytes.Equal(got, want) {
		t.Errorf("clock code\n got %x\nwant %x", got, want)
	}
	var table [16]uint32
	table[0], table[1], table[12] = 0xC000, 0xC010, 0xC000
	if clock.Table() != table {
		t.Errorf("clock table %x", clock.Table())
	}
	if pool := clock.Pool(); len(pool) != 27 || pool[26] != 60000000 {
		t.Errorf("clock pool %v", pool)
	}
	battery := specBatteryProgram()
	if got, want := battery.Code(), unhex(t, specBattery); !bytes.Equal(got, want) {
		t.Errorf("battery code\n got %x\nwant %x", got, want)
	}
	table = [16]uint32{}
	table[0], table[5] = 0xC000, 0xC000
	if battery.Table() != table {
		t.Errorf("battery table %x", battery.Table())
	}
	raise := specLayout()
	raise.Raise(raise.Const(12))
	if got := raise.Code(); !bytes.Equal(got, unhex(t, "42 f0 28 00")) {
		t.Errorf("RAISE C12: %x", got)
	}
}

func TestProgramEncoding(t *testing.T) {
	p := NewProgramAt(0x1000, 64)
	p.Add(R(3), R(1), 0x0120)
	if got := p.Code(); !bytes.Equal(got, unhex(t, "11 31 f0 20 01")) {
		t.Errorf("VM.md §2.1 example: %x", got)
	}
	back, ahead := p.Label(), p.Label()
	p.Bind(back)
	p.BgeS(R(1), 0x40, ahead)
	p.Jmp(back)
	p.Call(p.At(0x12345))
	p.Bind(ahead)
	p.Ret()
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	if got := p.Code()[5:]; !bytes.Equal(got, unhex(t, "3a 1f 54 11 00 40 00 30 45 11 00 31 45 23 01 32")) {
		t.Errorf("jumps: %x", got)
	}

	bad := NewProgramAt(0x2000, 64)
	bad.Jmp(bad.Label())
	if err := bad.Err(); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("unbound label: %v", err)
	}
	full := NewProgramAt(0x2000, 1)
	full.Const(1)
	full.Const(2)
	if err := full.Err(); err == nil || !strings.Contains(err.Error(), "pool is full") {
		t.Errorf("full pool: %v", err)
	}
	wrong := NewProgramAt(0x2000, 4)
	wrong.Load(12, R(0), R(1), R(2))
	if wrong.Err() == nil {
		t.Error("a 12-bit load was accepted")
	}
}
