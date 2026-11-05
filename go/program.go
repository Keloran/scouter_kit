package scouterkit

/*
#include <stdlib.h>
#include "scouter_kit_c.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// Word is an operand of the byte code: arena word i (bytes 4i...4i+3).
// Words 0...14 are the registers r0...r14, see R.
type Word uint16

// R is register n (0...14).
func R(n int) Word { return Word(n) }

// Label is a code address, possibly not known yet (see Program.Bind).
type Label int

// NoLabel is no label (Program.PanelPolicy without a redraw).
const NoLabel Label = -1

// Trigger IDs (VM.md §5.2).
type Trigger int

const (
	TriggerStart   Trigger = 0
	TriggerTimer   Trigger = 1
	TriggerMotion  Trigger = 2
	TriggerTilt    Trigger = 3
	TriggerLight   Trigger = 4
	TriggerBattery Trigger = 5
	TriggerCase    Trigger = 6
	TriggerWear    Trigger = 7
	TriggerTouch   Trigger = 8
	TriggerPeer    Trigger = 9
	TriggerUser0   Trigger = 12
	TriggerUser1   Trigger = 13
	TriggerUser2   Trigger = 14
	TriggerUser3   Trigger = 15
)

// Property IDs (VM.md §2.5).
type Property int

const (
	PropTimeUS         Property = 0x00
	PropTrigger        Property = 0x01
	PropPending        Property = 0x02
	PropArm            Property = 0x03
	PropLink           Property = 0x04
	PropLinkInterval   Property = 0x05
	PropBattMV         Property = 0x10
	PropBattPct        Property = 0x11
	PropCharge         Property = 0x12
	PropInCase         Property = 0x13
	PropWorn           Property = 0x14
	PropBattMA         Property = 0x15
	PropBattTemp       Property = 0x16
	PropAccX           Property = 0x18
	PropAccY           Property = 0x19
	PropAccZ           Property = 0x1A
	PropPitch          Property = 0x1B
	PropLight          Property = 0x1C
	PropRoll           Property = 0x1D
	PropBrightness     Property = 0x20
	PropPanel          Property = 0x21
	PropTimer          Property = 0x22
	PropMotionMG       Property = 0x23
	PropPitchLo        Property = 0x24
	PropPitchHi        Property = 0x25
	PropLightLo        Property = 0x26
	PropLightHi        Property = 0x27
	PropIMURate        Property = 0x28
	PropBrightnessMax  Property = 0x29
	PropBrightnessCap  Property = 0x2A
	PropGyrX           Property = 0x30
	PropGyrY           Property = 0x31
	PropGyrZ           Property = 0x32
	PropGyroRate       Property = 0x33
	PropTouchGesture   Property = 0x34
	PropCaseLid        Property = 0x35
	PropCaseCharging   Property = 0x36
	PropCaseBattPct    Property = 0x37
	PropAutoBrightness Property = 0x38
	PropPeerLink       Property = 0x39
	PropPeerSend       Property = 0x3A
	PropPeerMsg        Property = 0x3B
	PropPeerTUS        Property = 0x3C
)

// Unknown is the value of a property the arm does not have.
const Unknown = 0x80000000

// OneBPP is added to a BLIT mode for a 1-bpp source.
const OneBPP = 4

// Program is byte code of protocol 5 (VM.md §2): a program for the arena, or
// host code for Arm.Run. Its layout is fixed when it is made: a 16-word
// trigger table, a pool of constants (Const), the code. Errors (a bad label,
// a full pool) are kept and returned by Err.
type Program struct {
	c *C.sk_g1_program
}

// NewProgram makes a program with its trigger table at table, room for
// poolWords constants at pool, and its code from code (arena addresses).
func NewProgram(table, pool uint32, poolWords int, code uint32) *Program {
	p := &Program{c: C.sk_g1_program_new(C.uint32_t(table), C.uint32_t(pool), C.int(poolWords), C.uint32_t(code))}
	runtime.SetFinalizer(p, func(p *Program) { C.sk_g1_program_free(p.c) })
	return p
}

// NewProgramAt makes a program with the table at address, the pool after it
// and then the code.
func NewProgramAt(address uint32, poolWords int) *Program {
	return NewProgram(address, address+64, poolWords, address+64+4*uint32(poolWords))
}

// Label makes a label; Bind puts it at the next instruction.
func (p *Program) Label() Label {
	defer runtime.KeepAlive(p)
	return Label(C.sk_g1_program_label(p.c))
}

func (p *Program) Bind(l Label) {
	C.sk_g1_program_bind(p.c, C.int(l))
	runtime.KeepAlive(p)
}

// At is a label at a fixed address (code outside this program).
func (p *Program) At(address uint32) Label {
	defer runtime.KeepAlive(p)
	return Label(C.sk_g1_program_label_at(p.c, C.uint32_t(address)))
}

// On makes the code at l the handler of trigger t.
func (p *Program) On(t Trigger, l Label) {
	C.sk_g1_program_on(p.c, C.int(t), C.int(l))
	runtime.KeepAlive(p)
}

// PanelPolicy adds the display policy (VM.md §6.6): the panel off while
// the arm lies in the case (IN_CASE 1) or the glasses are folded (WORN 0,
// which the firmware debounces); meanwhile the program's Draw and Video show
// nothing (Video still decodes). It takes TriggerCase and TriggerWear and
// runs before the TriggerStart handler; when both have ended the next Draw
// shows, and it jumps to redraw (code that draws and ends; NoLabel: none).
// Call it before the first Draw or Video; it uses two pool words and up to
// five constants.
func (p *Program) PanelPolicy(redraw Label) {
	C.sk_g1_program_panel_policy(p.c, C.int(redraw))
	runtime.KeepAlive(p)
}

// Const is the pool word that holds v (added if needed).
func (p *Program) Const(v uint32) Word {
	defer runtime.KeepAlive(p)
	return Word(C.sk_g1_program_const(p.c, C.uint32_t(v)))
}

// Changes declares arena bytes the program writes: the library leaves them
// out of its checks (Arm.ArenaCRC) once the program is loaded.
func (p *Program) Changes(address, length uint32) {
	C.sk_g1_program_changes(p.c, C.uint32_t(address), C.uint32_t(length))
	runtime.KeepAlive(p)
}

// Here is the address of the next instruction.
func (p *Program) Here() uint32 {
	defer runtime.KeepAlive(p)
	return uint32(C.sk_g1_program_here(p.c))
}

func (p *Program) op(opcode int, label Label, operands ...Word) {
	var ptr *C.uint16_t
	if len(operands) > 0 {
		ptr = (*C.uint16_t)(unsafe.Pointer(&operands[0]))
	}
	C.sk_g1_program_op(p.c, C.int(opcode), ptr, C.int(len(operands)), C.int(label))
	runtime.KeepAlive(p)
	runtime.KeepAlive(operands)
}

// Op adds any instruction of VM.md §2.2 but WRITE and WRITE_RLE; target is
// for JMP, CALL and the branches (else -1).
func (p *Program) Op(opcode int, target Label, operands ...Word) { p.op(opcode, target, operands...) }

func (p *Program) Write(address uint32, data []byte) {
	var ptr *C.uint8_t
	if len(data) > 0 {
		ptr = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}
	C.sk_g1_program_write(p.c, C.uint32_t(address), ptr, C.size_t(len(data)))
	runtime.KeepAlive(p)
	runtime.KeepAlive(data)
}

func (p *Program) WriteRLE(address uint32, length int, packbits []byte) {
	var ptr *C.uint8_t
	if len(packbits) > 0 {
		ptr = (*C.uint8_t)(unsafe.Pointer(&packbits[0]))
	}
	C.sk_g1_program_write_rle(p.c, C.uint32_t(address), C.uint16_t(length), ptr, C.size_t(len(packbits)))
	runtime.KeepAlive(p)
	runtime.KeepAlive(packbits)
}

func (p *Program) Copy(dst, src, n Word)           { p.op(0x03, -1, dst, src, n) }
func (p *Program) Unpack(dst, src, n, length Word) { p.op(0x04, -1, dst, src, n, length) }
func (p *Program) Mov(d, a Word)                   { p.op(0x10, -1, d, a) }
func (p *Program) Add(d, a, b Word)                { p.op(0x11, -1, d, a, b) }
func (p *Program) Sub(d, a, b Word)                { p.op(0x12, -1, d, a, b) }
func (p *Program) Mul(d, a, b Word)                { p.op(0x13, -1, d, a, b) }
func (p *Program) DivU(d, a, b Word)               { p.op(0x14, -1, d, a, b) }
func (p *Program) ModU(d, a, b Word)               { p.op(0x15, -1, d, a, b) }
func (p *Program) DivS(d, a, b Word)               { p.op(0x16, -1, d, a, b) }
func (p *Program) ModS(d, a, b Word)               { p.op(0x17, -1, d, a, b) }
func (p *Program) And(d, a, b Word)                { p.op(0x18, -1, d, a, b) }
func (p *Program) Or(d, a, b Word)                 { p.op(0x19, -1, d, a, b) }
func (p *Program) Xor(d, a, b Word)                { p.op(0x1A, -1, d, a, b) }
func (p *Program) Shl(d, a, b Word)                { p.op(0x1B, -1, d, a, b) }
func (p *Program) Shr(d, a, b Word)                { p.op(0x1C, -1, d, a, b) }
func (p *Program) Sar(d, a, b Word)                { p.op(0x1D, -1, d, a, b) }

func width(bits int) int {
	switch bits {
	case 32:
		return 0
	case 16:
		return 1
	case 8:
		return 2
	case 4:
		return 3
	}
	return 0x7F // an invalid opcode: Err reports it
}

// Load reads element i (32, 16, 8 or 4 bits wide) from base into d.
func (p *Program) Load(bits int, d, base, i Word) { p.op(0x20+width(bits), -1, d, base, i) }

// Store writes the low bits of a to element i of base.
func (p *Program) Store(bits int, base, i, a Word) { p.op(0x24+width(bits), -1, base, i, a) }

func (p *Program) Jmp(l Label)               { p.op(0x30, l) }
func (p *Program) Call(l Label)              { p.op(0x31, l) }
func (p *Program) Ret()                      { p.op(0x32, -1) }
func (p *Program) End()                      { p.op(0x33, -1) }
func (p *Program) Halt()                     { p.op(0x34, -1) }
func (p *Program) Beq(a, b Word, l Label)    { p.op(0x35, l, a, b) }
func (p *Program) Bne(a, b Word, l Label)    { p.op(0x36, l, a, b) }
func (p *Program) BltU(a, b Word, l Label)   { p.op(0x37, l, a, b) }
func (p *Program) BgeU(a, b Word, l Label)   { p.op(0x38, l, a, b) }
func (p *Program) BltS(a, b Word, l Label)   { p.op(0x39, l, a, b) }
func (p *Program) BgeS(a, b Word, l Label)   { p.op(0x3A, l, a, b) }
func (p *Program) SleepUntil(t Word)         { p.op(0x40, -1, t) }
func (p *Program) WaitEvent(d, mask Word)    { p.op(0x41, -1, d, mask) }
func (p *Program) Raise(t Word)              { p.op(0x42, -1, t) }
func (p *Program) Notify(a, b Word)          { p.op(0x43, -1, a, b) }
func (p *Program) SysRead(d, property Word)  { p.op(0x44, -1, d, property) }
func (p *Program) SysWrite(property, a Word) { p.op(0x45, -1, property, a) }

func (p *Program) Fill(dst, stride, x, y, w, h, level Word) {
	p.op(0x50, -1, dst, stride, x, y, w, h, level)
}

// Blit's mode is a Mode, plus OneBPP for a 1-bpp source.
func (p *Program) Blit(src, srcStride, srcX, srcY, dst, dstStride, dstX, dstY, w, h, mode, level Word) {
	p.op(0x51, -1, src, srcStride, srcX, srcY, dst, dstStride, dstX, dstY, w, h, mode, level)
}

func (p *Program) Glyphs(font, dst, stride, x, y, mode, level, text, n Word) {
	p.op(0x52, -1, font, dst, stride, x, y, mode, level, text, n)
}

func (p *Program) Draw(src, srcStride, srcX, srcY, dstX, dstY, w, h Word) {
	p.op(0x53, -1, src, srcStride, srcX, srcY, dstX, dstY, w, h)
}

func (p *Program) Line(dst, stride, x0, y0, x1, y1, level Word) {
	p.op(0x54, -1, dst, stride, x0, y0, x1, y1, level)
}

// Video decodes the record of n bytes at src into the 1-bpp bitmap and
// draws the rows that changed at (x, y) (VM.md §3.7).
func (p *Program) Video(bitmap, stride, w, h, x, y, level, src, n, state Word) {
	p.op(0x55, -1, bitmap, stride, w, h, x, y, level, src, n, state)
}

// AssetLoad copies bytes off ... off+n-1 of the stored asset whose 16-byte ID
// is at address id to dst (VM.md §2.4; Store puts assets on the arms).
func (p *Program) AssetLoad(id, dst, off, n Word) {
	p.op(C.SK_G1_OP_ASSET_LOAD, -1, id, dst, off, n)
}

// Err is nil if the program can be loaded: every label bound, the pool big
// enough, operand counts right, the code inside the arena.
func (p *Program) Err() error {
	defer runtime.KeepAlive(p)
	var msg [160]C.char
	if C.sk_g1_program_valid(p.c, &msg[0], C.size_t(len(msg))) != 0 {
		return nil
	}
	return errors.New("scouterkit: program: " + C.GoString(&msg[0]))
}

// Code is the instructions, from the code address.
func (p *Program) Code() []byte {
	defer runtime.KeepAlive(p)
	var n C.size_t
	b := C.sk_g1_program_code(p.c, &n)
	return C.GoBytes(unsafe.Pointer(b), C.int(n))
}

// Pool is the constants, from the pool address.
func (p *Program) Pool() []uint32 {
	defer runtime.KeepAlive(p)
	var n C.size_t
	b := C.sk_g1_program_pool(p.c, &n)
	if n == 0 {
		return nil
	}
	return append([]uint32(nil), unsafe.Slice((*uint32)(unsafe.Pointer(b)), int(n))...)
}

// Table is the handler address per trigger (0: none).
func (p *Program) Table() [16]uint32 {
	defer runtime.KeepAlive(p)
	var t [16]uint32
	copy(t[:], unsafe.Slice((*uint32)(unsafe.Pointer(C.sk_g1_program_table(p.c))), 16))
	return t
}
