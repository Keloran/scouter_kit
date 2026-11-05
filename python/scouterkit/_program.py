from __future__ import annotations

import ctypes
from enum import IntEnum

from . import _c
from .errors import ProgramError

Word = int
"""An operand of the byte code: arena word i (bytes 4i...4i+3). Words 0...14 are r0...r14."""

Label = int
"""A code address, maybe not known yet (Program.bind)."""

UNKNOWN = 0x80000000
"""The value of a property the arm does not have."""

ONE_BPP = 4
"""Added to a BLIT mode for a 1-bpp source."""


def R(n: int) -> Word:
    """Register n (0...14)."""
    if not 0 <= n <= 14:
        raise ValueError(f"no register r{n}")
    return n


class Trigger(IntEnum):
    """Trigger IDs (VM.md §5.2)."""

    START = 0
    TIMER = 1
    MOTION = 2
    TILT = 3
    LIGHT = 4
    BATTERY = 5
    CASE = 6
    WEAR = 7
    TOUCH = 8
    PEER = 9
    USER0 = 12
    USER1 = 13
    USER2 = 14
    USER3 = 15


class Prop(IntEnum):
    """Property IDs (VM.md §2.5)."""

    TIME_US = 0x00
    TRIGGER = 0x01
    PENDING = 0x02
    ARM = 0x03
    LINK = 0x04
    LINK_INTERVAL = 0x05
    BATT_MV = 0x10
    BATT_PCT = 0x11
    CHARGE = 0x12
    IN_CASE = 0x13
    WORN = 0x14
    BATT_MA = 0x15
    BATT_TEMP = 0x16
    ACC_X = 0x18
    ACC_Y = 0x19
    ACC_Z = 0x1A
    PITCH = 0x1B
    LIGHT = 0x1C
    ROLL = 0x1D
    BRIGHTNESS = 0x20
    PANEL = 0x21
    TIMER = 0x22
    MOTION_MG = 0x23
    PITCH_LO = 0x24
    PITCH_HI = 0x25
    LIGHT_LO = 0x26
    LIGHT_HI = 0x27
    IMU_RATE = 0x28
    BRIGHTNESS_MAX = 0x29
    BRIGHTNESS_CAP = 0x2A
    GYR_X = 0x30
    GYR_Y = 0x31
    GYR_Z = 0x32
    GYRO_RATE = 0x33
    TOUCH_GESTURE = 0x34
    CASE_LID = 0x35
    CASE_CHARGING = 0x36
    CASE_BATT_PCT = 0x37
    AUTO_BRIGHTNESS = 0x38
    PEER_LINK = 0x39
    PEER_SEND = 0x3A
    PEER_MSG = 0x3B
    PEER_T_US = 0x3C


class Mode(IntEnum):
    """How BLIT and GLYPHS combine pixels (VM.md §3.1)."""

    COPY = 0
    MAX = 1
    MASK = 2
    ERASE = 3


_WIDTHS = {32: 0, 16: 1, 8: 2, 4: 3}


class Program:
    """Byte code of protocol 5 (VM.md §2): a program for the arena, or host code for Arm.run.

    The layout is fixed when it is made: a 16-word trigger table at table, a pool of
    pool_words constants (const) at pool, then the code. pool and code default to right
    after the table and the pool.
    """

    def __init__(self, table: int, pool_words: int = 16, *, pool: int | None = None,
                 code: int | None = None) -> None:
        self.table_address = table
        self.pool_address = table + 64 if pool is None else pool
        self.code_address = self.pool_address + 4 * pool_words if code is None else code
        self.pool_words = pool_words
        self._lib = _c.lib()
        self._ptr = self._lib.sk_g1_program_new(table, self.pool_address, pool_words,
                                                self.code_address)

    def __del__(self) -> None:
        if getattr(self, "_ptr", None):
            self._lib.sk_g1_program_free(self._ptr)
            self._ptr = None

    def label(self) -> Label:
        """A new label; bind() puts it at the next instruction."""
        return self._lib.sk_g1_program_label(self._ptr)

    def bind(self, label: Label) -> None:
        self._lib.sk_g1_program_bind(self._ptr, label)

    def at(self, address: int) -> Label:
        """A label at a fixed address (code outside this program)."""
        return self._lib.sk_g1_program_label_at(self._ptr, address)

    def on(self, trigger: Trigger, label: Label) -> None:
        """Makes the code at label the handler of the trigger."""
        self._lib.sk_g1_program_on(self._ptr, trigger, label)

    def const(self, value: int) -> Word:
        """The pool word that holds value (added if needed); negative values as int32."""
        return self._lib.sk_g1_program_const(self._ptr, value & 0xFFFFFFFF)

    def changes(self, address: int, length: int) -> None:
        """Declares arena bytes the program writes: the library leaves them out of its checks
        (Arm.arena_crc) once the program is loaded."""
        self._lib.sk_g1_program_changes(self._ptr, address, length)

    @property
    def here(self) -> int:
        """The address of the next instruction."""
        return self._lib.sk_g1_program_here(self._ptr)

    @property
    def error(self) -> str | None:
        """None if the program can be loaded: every label bound, the pool big enough, operand
        counts right, the code inside the arena."""
        text = ctypes.create_string_buffer(160)
        if self._lib.sk_g1_program_valid(self._ptr, text, len(text)):
            return None
        return text.value.decode(errors="replace")

    def validate(self) -> None:
        """Raises ProgramError if the program cannot be loaded."""
        if (error := self.error) is not None:
            raise ProgramError(f"program: {error}")

    @property
    def code(self) -> bytes:
        """The instructions, from the code address."""
        n = ctypes.c_size_t()
        data = self._lib.sk_g1_program_code(self._ptr, ctypes.byref(n))
        return ctypes.string_at(data, n.value) if n.value else b""

    @property
    def pool(self) -> list[int]:
        """The constants, from the pool address."""
        n = ctypes.c_size_t()
        data = self._lib.sk_g1_program_pool(self._ptr, ctypes.byref(n))
        return data[:n.value] if n.value else []

    @property
    def table(self) -> list[int]:
        """The handler address per trigger (0: none)."""
        return self._lib.sk_g1_program_table(self._ptr)[:16]

    def op(self, opcode: int, *operands: Word, target: Label = -1) -> None:
        """Any instruction of VM.md §2.2 but WRITE and WRITE_RLE; target is for JMP, CALL and
        the branches."""
        for w in operands:
            if not 0 <= w <= 0xFFFF:
                raise ValueError(f"operand {w} is not an arena word")
        words = (ctypes.c_uint16 * len(operands))(*operands) if operands else None
        self._lib.sk_g1_program_op(self._ptr, opcode, words, len(operands), target)

    def write(self, address: int, data: bytes) -> None:
        data = bytes(data)
        self._lib.sk_g1_program_write(self._ptr, address, data, len(data))

    def write_rle(self, address: int, length: int, packbits: bytes) -> None:
        packbits = bytes(packbits)
        self._lib.sk_g1_program_write_rle(self._ptr, address, length, packbits, len(packbits))

    def copy(self, dst: Word, src: Word, n: Word) -> None:
        self.op(0x03, dst, src, n)

    def unpack(self, dst: Word, src: Word, n: Word, length: Word) -> None:
        self.op(0x04, dst, src, n, length)

    def mov(self, d: Word, a: Word) -> None:
        self.op(0x10, d, a)

    def add(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x11, d, a, b)

    def sub(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x12, d, a, b)

    def mul(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x13, d, a, b)

    def divu(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x14, d, a, b)

    def modu(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x15, d, a, b)

    def divs(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x16, d, a, b)

    def mods(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x17, d, a, b)

    def and_(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x18, d, a, b)

    def or_(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x19, d, a, b)

    def xor(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x1A, d, a, b)

    def shl(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x1B, d, a, b)

    def shr(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x1C, d, a, b)

    def sar(self, d: Word, a: Word, b: Word) -> None:
        self.op(0x1D, d, a, b)

    def load(self, bits: int, d: Word, base: Word, i: Word) -> None:
        """d = element i (32, 16, 8 or 4 bits wide) of base."""
        self.op(0x20 + self._width(bits), d, base, i)

    def store(self, bits: int, base: Word, i: Word, a: Word) -> None:
        """Element i of base = the low bits of a."""
        self.op(0x24 + self._width(bits), base, i, a)

    @staticmethod
    def _width(bits: int) -> int:
        if bits not in _WIDTHS:
            raise ValueError(f"loads and stores are 32, 16, 8 or 4 bits wide, not {bits}")
        return _WIDTHS[bits]

    def jmp(self, label: Label) -> None:
        self.op(0x30, target=label)

    def call(self, label: Label) -> None:
        self.op(0x31, target=label)

    def ret(self) -> None:
        self.op(0x32)

    def end(self) -> None:
        self.op(0x33)

    def halt(self) -> None:
        self.op(0x34)

    def beq(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x35, a, b, target=label)

    def bne(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x36, a, b, target=label)

    def bltu(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x37, a, b, target=label)

    def bgeu(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x38, a, b, target=label)

    def blts(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x39, a, b, target=label)

    def bges(self, a: Word, b: Word, label: Label) -> None:
        self.op(0x3A, a, b, target=label)

    def sleep_until(self, t: Word) -> None:
        self.op(0x40, t)

    def wait_event(self, d: Word, mask: Word) -> None:
        self.op(0x41, d, mask)

    def raise_(self, trigger: Word) -> None:
        self.op(0x42, trigger)

    def notify(self, a: Word, b: Word) -> None:
        self.op(0x43, a, b)

    def sys_read(self, d: Word, prop: Word) -> None:
        self.op(0x44, d, prop)

    def sys_write(self, prop: Word, a: Word) -> None:
        self.op(0x45, prop, a)

    def fill(self, dst: Word, stride: Word, x: Word, y: Word, w: Word, h: Word,
             level: Word) -> None:
        self.op(0x50, dst, stride, x, y, w, h, level)

    def blit(self, src: Word, src_stride: Word, src_x: Word, src_y: Word, dst: Word,
             dst_stride: Word, dst_x: Word, dst_y: Word, w: Word, h: Word, mode: Word,
             level: Word) -> None:
        """mode: a Mode, plus ONE_BPP for a 1-bpp source."""
        self.op(0x51, src, src_stride, src_x, src_y, dst, dst_stride, dst_x, dst_y, w, h, mode,
                level)

    def glyphs(self, font: Word, dst: Word, stride: Word, x: Word, y: Word, mode: Word,
               level: Word, text: Word, n: Word) -> None:
        self.op(0x52, font, dst, stride, x, y, mode, level, text, n)

    def draw(self, src: Word, src_stride: Word, src_x: Word, src_y: Word, dst_x: Word,
             dst_y: Word, w: Word, h: Word) -> None:
        self.op(0x53, src, src_stride, src_x, src_y, dst_x, dst_y, w, h)

    def line(self, dst: Word, stride: Word, x0: Word, y0: Word, x1: Word, y1: Word,
             level: Word) -> None:
        self.op(0x54, dst, stride, x0, y0, x1, y1, level)

    def video(self, bitmap: Word, stride: Word, w: Word, h: Word, x: Word, y: Word, level: Word,
              src: Word, n: Word, state: Word) -> None:
        self.op(0x55, bitmap, stride, w, h, x, y, level, src, n, state)

    def asset_load(self, id: Word, dst: Word, off: Word, n: Word) -> None:
        """Bytes off ... off+n-1 of the stored asset whose 16-byte ID is at address id, to dst."""
        self.op(0x56, id, dst, off, n)
