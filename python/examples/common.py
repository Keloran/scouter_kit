"""What the examples share: the command line, the geometry and panel checks."""

from __future__ import annotations

import argparse
import struct
import time
import zlib

from PIL import Image

import scouterkit as sk


def parser(description: str) -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=description)
    p.add_argument("--left", default="", help='left arm address (empty: the bonded glasses, "none": no left arm)')
    p.add_argument("--right", default="", help='right arm address (empty: the bonded glasses, "none": no right arm)')
    p.add_argument("--adapter", default="", help='BlueZ adapter, "hci1" or its address (empty: the first powered one)')
    p.add_argument("--emulator", action="store_true", help="two emulated arms instead of the glasses")
    p.add_argument("--log", action="store_true", help="the library logs to stderr")
    return p


def connect(args: argparse.Namespace, **options: object) -> sk.Session:
    return sk.init(left=args.left, right=args.right, adapter=args.adapter, log=args.log,
                   emulator=args.emulator or None, **options)


def present() -> list[sk.Arm]:
    """The arms the library talks to."""
    return [arm for arm in (sk.left, sk.right) if arm.state().present]


def panel_at(session: sk.Session, arm: sk.Arm, x: int, y: int) -> tuple[int, int]:
    """Where an eye shows virtual display pixel (x, y)."""
    if arm is sk.left:
        return x - max(0, -session.dx), y + max(0, -session.dy)
    return x - max(0, session.dx), y + max(0, session.dy)


def panel_rows(image: Image.Image, x: int, rows: int) -> bytes:
    """Panel rows (4 bpp, 320 bytes each, the left pixel in the high nibble) that show the
    image at column x and nothing else."""
    out = bytearray(rows * sk.PANEL_WIDTH // 2)
    width = min(image.width, sk.PANEL_WIDTH - x)
    pixels = image.load()
    for r in range(min(rows, image.height)):
        for c in range(width):
            level = pixels[c, r] // 17
            px = x + c
            out[r * sk.PANEL_WIDTH // 2 + px // 2] |= level << 4 if px % 2 == 0 else level
    return bytes(out)


def check_panel(arm: sk.Arm, image: Image.Image, x: int, y: int) -> None:
    """Reads the panel rows y... back and compares them with the image at (x, y)."""
    got, _ = arm.panel_crc(y, image.height)
    want = zlib.crc32(panel_rows(image, x, image.height))
    if got != want:
        raise RuntimeError(f"{arm.name} arm: panel rows {y}...{y + image.height - 1} read back "
                           f"{got:08x}, expected {want:08x}")


def wait_notify(arm: sk.Arm, timeout: float) -> sk.Event:
    """The arm's next NOTIFY; a HALT, a refused command or a reset is an error."""
    deadline = time.monotonic() + timeout
    while (e := arm.next_event(max(0.0, deadline - time.monotonic()))) is not None:
        if e.type == sk.EventType.NOTIFY:
            return e
        if e.type == sk.EventType.HALT:
            raise RuntimeError(f"{arm.name} arm: the program halted: code {e.code}, "
                               f"op 0x{e.op:02x} at 0x{e.address:05x}, detail 0x{e.detail:x}")
        if e.type == sk.EventType.ERROR:
            raise RuntimeError(f"{arm.name} arm: host code refused: code {e.code}, op 0x{e.op:02x}")
        if e.type == sk.EventType.RESET:
            raise RuntimeError(f"{arm.name} arm: it rebooted (session reset)")
    raise TimeoutError(f"{arm.name} arm: no NOTIFY within {timeout} s")


def drain(arm: sk.Arm) -> None:
    while arm.next_event() is not None:
        pass


def lead(arms: list[sk.Arm]) -> float:
    """How far ahead to schedule what both eyes show: two connection intervals, the clock
    error and 15 ms (VM.md §7), in seconds."""
    us = 30_000
    for arm in arms:
        s = arm.state()
        us = max(us, 2 * s.interval_us + s.clock_error_us + 15_000)
    return us / 1e6


def words(*values: int) -> bytes:
    """The values as little-endian arena words."""
    return struct.pack(f"<{len(values)}I", *(v & 0xFFFFFFFF for v in values))


def varints(glyphs: list[int]) -> bytes:
    """Glyph indices as GLYPHS reads them (LEB128)."""
    out = bytearray()
    for g in glyphs:
        while g >= 0x80:
            out.append(g & 0x7F | 0x80)
            g >>= 7
        out.append(g)
    return bytes(out)
