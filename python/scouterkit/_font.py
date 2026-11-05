from __future__ import annotations

import ctypes
import functools
import os
from dataclasses import dataclass
from enum import IntEnum
from pathlib import Path

from . import _c
from ._core import Bitmap
from .errors import ScouterKitError


class Align(IntEnum):
    LEFT = 0
    CENTER = 1
    RIGHT = 2


@dataclass(frozen=True)
class Extent:
    """The size of a line of text."""

    width: int  # advance width, kerning included
    height: int  # height of the ink
    ascent: int  # ink above the baseline
    descent: int  # ink below the baseline


@functools.cache
def departure_mono() -> bytes:
    """The built-in font blob: Departure Mono 11 px (SIL Open Font License 1.1)."""
    size = ctypes.c_size_t()
    data = _c.lib().sk_py_departure_mono(ctypes.byref(size))
    return ctypes.string_at(data, size.value)


class Font:
    """A font in the glasses' memory, until unload(), unload_all() or deinit().
    As a context manager it unloads itself at the end."""

    def __init__(self, handle: int, ascent: int, descent: int, line_spacing: int) -> None:
        self._ptr = handle
        self._generation = _c.generation
        self.ascent = ascent  # pixels above the baseline
        self.descent = descent  # pixels below the baseline
        self.line_spacing = line_spacing  # baseline to baseline

    @property
    def loaded(self) -> bool:
        return bool(self._ptr) and self._generation == _c.generation

    def _handle(self) -> int:
        if not self.loaded:
            raise ScouterKitError("the font is unloaded")
        return self._ptr

    def measure(self, text: str) -> Extent:
        """The size of a line of UTF-8 text."""
        out = [ctypes.c_int() for _ in range(4)]
        with _c.lock.shared():
            _c.lib().sk_font_measure(self._handle(), text.encode(), *map(ctypes.byref, out))
        return Extent(*(v.value for v in out))

    def draw_line(self, bitmap: Bitmap, text: str, x: int, baseline: int,
                  align: Align = Align.LEFT, gray: int = 255) -> int:
        """Draws one line of text into the bitmap; the arms draw the glyphs themselves. x is
        the left edge, the centre or the right edge of the text, as align says. Returns the
        advance width."""
        with _c.lock.shared():
            return _c.lib().sk_font_draw_line(self._handle(), bitmap._handle(), text.encode(), x,
                                              baseline, align, gray)

    @property
    def address(self) -> int:
        """Where the font blob is in both arms' arena, for programs."""
        with _c.lock.shared():
            return _c.lib().sk_g1_font_address(self._handle())

    def glyph_indices(self, text: str) -> list[int]:
        """The font's glyphs for the text as GLYPHS draws them: a digit's index is its value
        (VM.md §4.1); a missing character is U+FFFD's glyph, or left out."""
        data = text.encode()
        with _c.lock.shared():
            lib = _c.lib()
            n = lib.sk_g1_glyph_indices(self._handle(), data, None, 0)
            out = (ctypes.c_uint32 * n)()
            lib.sk_g1_glyph_indices(self._handle(), data, out, n)
        return list(out)

    def unload(self) -> None:
        """Frees the font's memory on the glasses."""
        with _c.lock.exclusive():
            if self.loaded:
                _c.lib().sk_unload_font(self._ptr)
            self._ptr = 0

    def __enter__(self) -> Font:
        return self

    def __exit__(self, *exc: object) -> None:
        self.unload()

    def __repr__(self) -> str:
        state = "" if self.loaded else ", unloaded"
        return f"<Font ascent {self.ascent}, descent {self.descent}{state}>"


def load_font(blob: bytes | str | os.PathLike[str], glyphs: str = "", scale: int = 1,
              stored: bool = False) -> Font:
    """Loads a font blob made by font_converter.py (bytes, or the path of a .skf file).
    glyphs is the text of the characters to load (empty: all of them); the space and U+FFFD
    are added. scale > 1 enlarges the glyphs of pixel fonts. stored: also keep it in each
    arm's flash (VM.md §6.10), so a reboot or a later session loads it from there."""
    if isinstance(blob, (str, os.PathLike)):
        blob = Path(blob).read_bytes()
    data = bytes(blob)
    if not data:
        raise ValueError("empty font blob")
    error = ctypes.create_string_buffer(256)
    with _c.lock.shared():
        _c.require_init()
        lib = _c.lib()
        handle = lib.sk_load_font_keep(data, len(data), glyphs.encode(), scale, int(stored), error,
                                       len(error))
        if not handle:
            raise ScouterKitError(error.value.decode(errors="replace"))
        metrics = [ctypes.c_int() for _ in range(3)]
        lib.sk_font_metrics(handle, *map(ctypes.byref, metrics))
        return Font(handle, *(v.value for v in metrics))
