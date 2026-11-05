"""VIDEO records (VM.md §3.7) from 1-bpp frames."""

from __future__ import annotations

import ctypes

from . import _c

VIDEO_STATE = 1596


class VideoEncoder:
    """Makes VIDEO records for an arm whose 1-bpp bitmap starts zeroed: give every record to
    the arm, in order; the first one resets the decoder's state."""

    def __init__(self, width: int, height: int) -> None:
        if not (1 <= width <= 640 and 1 <= height <= 480):
            raise ValueError("size out of range: 1...640 x 1...480")
        self.width, self.height = width, height
        self._lib = _c.lib()
        self._ptr = self._lib.sk_g1_video_encoder_new(width, height)

    def __del__(self) -> None:
        if getattr(self, "_ptr", None):
            self._lib.sk_g1_video_encoder_free(self._ptr)
            self._ptr = None

    @property
    def stride(self) -> int:
        """The least bytes per row of the arm's bitmap."""
        return (self.width + 7) // 8

    def encode(self, pixels: bytes, redraw: bool = False, max_bytes: int = 0) -> bytes | None:
        """The record of the next frame: width x height bytes, nonzero = ink. redraw: the arm
        draws every row. A record over max_bytes (if > 0) is not made: None, the encoder
        unchanged."""
        pixels = bytes(pixels)
        if len(pixels) < self.width * self.height:
            raise ValueError("frame too small")
        n = ctypes.c_size_t()
        limit = max_bytes if max_bytes > 0 else (1 << 62)
        p = self._lib.sk_g1_video_encode(self._ptr, pixels, int(redraw), limit, ctypes.byref(n))
        if not p:
            return None
        return ctypes.string_at(p, n.value)
