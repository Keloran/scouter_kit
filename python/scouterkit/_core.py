from __future__ import annotations

import atexit
import ctypes
import math
from collections.abc import Sequence
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Union

from PIL import Image

if TYPE_CHECKING:
    import numpy.typing as npt

from . import _c, _mic
from .errors import (BluetoothError, BondingError, DeviceMemoryError, FirmwareError,
                     NoG1FoundError, NotInitializedError, ScouterKitError)

PANEL_WIDTH = 640
PANEL_HEIGHT = 480
ARENA_BYTES = 262144

ImageLike = Union[Image.Image, "npt.ArrayLike"]
"""A Pillow image, or an array: H x W gray, H x W x 3 RGB or H x W x 4 RGBA; uint8, bool or
floats in 0...1."""

_INIT_ERRORS = {1: BluetoothError, 2: NoG1FoundError, 3: BondingError, 4: FirmwareError}


@dataclass(frozen=True)
class Session:
    """The connection made by init(); as a context manager, deinit() at its end."""

    dx: int
    dy: int
    left: str
    right: str
    adapter: str
    keep_sessions: bool
    emulator: bool

    @property
    def size(self) -> tuple[int, int]:
        """The virtual display: (640 + |dx|) x (480 - |dy|)."""
        return PANEL_WIDTH + abs(self.dx), PANEL_HEIGHT - abs(self.dy)

    def __enter__(self) -> Session:
        return self

    def __exit__(self, *exc: object) -> None:
        deinit()


def init(*, dx: int = 100, dy: int = 9, left: str = "", right: str = "", adapter: str = "",
         timeout: float = 30.0, keep_sessions: bool = False, log: bool = False,
         emulator: bool | None = None) -> Session:
    """Connects to the glasses and returns once both arms are ready.

    The display is (640 + |dx|) x (480 - |dy|): the left eye shows columns 0...639 on its
    panel rows 0...470, the right eye columns dx...dx+639 on its rows dy...479 (dx 100, dy 9:
    the user's calibration). left and right are the arms' addresses: empty for the bonded
    glasses, "none" to leave an arm out. adapter is the BlueZ adapter ("hci1" or its
    address), empty for the first powered one. keep_sessions keeps the arms' sessions (a
    program on them runs on): the library then owns no memory and draws nothing, for tools
    that use control requests, events and the microphone only. log makes the library log to
    stderr. emulator: True loads the library with two emulated arms, False the glasses'; None
    keeps the one already loaded (else the glasses').
    """
    lib = _c.load(emulator)
    with _c.lock.exclusive():
        if _c.session is not None:
            raise ScouterKitError("already initialized")
        config = _c.Config(dx, dy, left.encode(), right.encode(), adapter.encode(),
                           round(timeout * 1000), int(keep_sessions), int(log))
        error = ctypes.create_string_buffer(256)
        code = lib.sk_g1_init_config(ctypes.byref(config), error, len(error))
        if code:
            raise _INIT_ERRORS.get(code, BluetoothError)(error.value.decode(errors="replace"))
        _c.session = Session(dx, dy, left, right, adapter, keep_sessions, _c.is_emulator(lib))
        return _c.session


def deinit() -> None:
    """Switches the microphones off, waits until the last update is shown, disconnects and
    unloads every font and bitmap."""
    with _c.lock.exclusive():
        if _c.session is None:
            return
        lib = _c.lib()
        state = _c.MicState()
        for arm in (0, 1):
            lib.sk_g1_mic_get_state(arm, ctypes.byref(state))
            if state.on:
                lib.sk_g1_mic_stop(arm)
        _mic._unguard(0, 1)
        lib.sk_g1_deinit()
        _c.session = None
        _c.generation += 1


atexit.register(deinit)


def size() -> tuple[int, int]:
    """The display's size, (0, 0) before init()."""
    with _c.lock.shared():
        v = _c.lib().sk_display()
        return v.width, v.height


def draw(bitmap: Bitmap, x: int, y: int) -> None:
    """Places the bitmap on the display with its top left corner at (x, y); later calls
    draw on top. flush() shows it."""
    with _c.lock.shared():
        _c.lib().sk_draw(bitmap._handle(), x, y)


def clear() -> None:
    """Places black over the whole display."""
    with _c.lock.shared():
        _c.lib().sk_clear()


def flush() -> None:
    """Uploads the changed bitmap pixels and shows what draw() placed since the last flush on
    both eyes at the same moment. Returns once queued, after the previous update is shown."""
    with _c.lock.shared():
        _c.lib().sk_flush()


def display() -> Image.Image:
    """A copy of what the eyes show, as of the last flush()."""
    with _c.lock.exclusive():
        v = _c.lib().sk_display()
        return Image.frombytes("L", (max(v.width, 0), max(v.height, 0)), _c.view_bytes(v))


def total_memory() -> int:
    """The glasses' memory for fonts and bitmaps, in bytes per arm."""
    with _c.lock.shared():
        return _c.lib().sk_total_memory()


def free_memory() -> int:
    with _c.lock.shared():
        return _c.lib().sk_free_memory()


def unload_all() -> None:
    """Unloads every font and bitmap."""
    with _c.lock.exclusive():
        _c.lib().sk_unload_all()
        _c.generation += 1


def alloc(n: int) -> int:
    """Reserves n bytes of arena memory at the same address on both arms (a multiple of 4),
    shared with the fonts and bitmaps."""
    with _c.lock.shared():
        _c.require_init()
        address = _c.lib().sk_g1_alloc(n)
    if address < 0:
        raise DeviceMemoryError(f"{n} bytes: out of device memory")
    return address


def free(address: int) -> None:
    """Releases memory from alloc()."""
    with _c.lock.shared():
        _c.lib().sk_g1_free(address)


def set_response(curve: Sequence[int] | bytes) -> None:
    """Sets the level response curve (256 entries) that load_bitmap() and Bitmap.write()
    apply before dithering. It starts as the identity."""
    data = bytes(curve)
    if len(data) != 256:
        raise ValueError("the response curve has 256 entries")
    with _c.lock.exclusive():
        ctypes.memmove(_c.lib().sk_response(), data, 256)


def response() -> bytes:
    with _c.lock.exclusive():
        return ctypes.string_at(_c.lib().sk_response(), 256)


def gamma_response(gamma: float) -> bytes:
    """The curve 255 * (v / 255) ** gamma. The G1's levels are roughly linear in luminance, so
    a gamma near 2.2 moves picture midtones to the low levels, where the steps are visible."""
    return bytes(math.floor(255 * (v / 255) ** gamma + 0.5) for v in range(256))


def to_gray(image: ImageLike) -> Image.Image:
    """The image's luma as an "L" image, composited over black (black is see-through)."""
    if not isinstance(image, Image.Image):
        image = _from_array(image)
    if image.mode == "L":
        return image
    if image.mode.startswith("I;16"):
        return image.convert("I").point(lambda v: v / 257).convert("L")
    if image.mode in ("RGBA", "LA", "PA", "RGBa", "La") or "transparency" in image.info:
        rgba = image.convert("RGBA")
        image = Image.alpha_composite(Image.new("RGBA", rgba.size, (0, 0, 0, 255)), rgba)
    return image.convert("L")


def _from_array(array: Any) -> Image.Image:
    import numpy as np

    a = np.asarray(array)
    if a.dtype == np.bool_:
        a = a.astype(np.uint8) * 255
    elif a.dtype.kind == "f":
        a = np.rint(np.clip(a, 0, 1) * 255).astype(np.uint8)
    elif a.dtype != np.uint8:
        a = np.clip(a, 0, 255).astype(np.uint8)
    if a.ndim == 3 and a.shape[2] == 1:
        a = a[:, :, 0]
    if not (a.ndim == 2 or a.ndim == 3 and a.shape[2] in (3, 4)):
        raise ValueError(f"an image array is H x W, H x W x 3 or H x W x 4, not {a.shape}")
    return Image.fromarray(np.ascontiguousarray(a))


class Bitmap:
    """A gray image in the glasses' memory, until unload(), unload_all() or deinit().
    As a context manager it unloads itself at the end."""

    def __init__(self, handle: int, width: int, height: int) -> None:
        self._ptr = handle
        self._generation = _c.generation
        self.width = width
        self.height = height

    @property
    def size(self) -> tuple[int, int]:
        return self.width, self.height

    @property
    def loaded(self) -> bool:
        return bool(self._ptr) and self._generation == _c.generation

    def _handle(self) -> int:
        if not self.loaded:
            raise ScouterKitError("the bitmap is unloaded")
        return self._ptr

    def write(self, image: ImageLike, x: int = 0, y: int = 0, dither: bool = True) -> None:
        """Puts the image into the bitmap with its top left corner at (x, y): its luma goes
        through the response curve and Floyd-Steinberg dithering to the 16 levels. With the
        identity curve, pixels that are multiples of 17 are kept exactly."""
        gray = to_gray(image)
        if not gray.width or not gray.height:
            return
        with _c.lock.shared():
            _c.lib().sk_write(self._handle(), gray.tobytes(), gray.width, gray.height, gray.width,
                              x, y, int(dither))

    def fill(self, box: tuple[int, int, int, int], gray: int) -> None:
        """Sets the pixels of box = (x0, y0, x1, y1), clipped to the bitmap, to gray."""
        x0, y0, x1, y1 = box
        x0, x1 = sorted((x0, x1))
        y0, y1 = sorted((y0, y1))
        if x0 == x1 or y0 == y1:
            return
        with _c.lock.shared():
            _c.lib().sk_fill(self._handle(), x0, y0, x1 - x0, y1 - y0, gray)

    def image(self) -> Image.Image:
        """A copy of the bitmap's pixels."""
        with _c.lock.exclusive():
            v = _c.lib().sk_bitmap_contents(self._handle())
            return Image.frombytes("L", (v.width, v.height), _c.view_bytes(v))

    @property
    def address(self) -> int:
        """Where the bitmap is in both arms' arena (4 bpp), for programs."""
        return self._address()[0]

    @property
    def stride(self) -> int:
        """Bytes per row in the arena."""
        return self._address()[1]

    def _address(self) -> tuple[int, int]:
        stride = ctypes.c_int()
        with _c.lock.shared():
            address = _c.lib().sk_g1_bitmap_address(self._handle(), ctypes.byref(stride))
        return address, stride.value

    def unload(self) -> None:
        """Frees the bitmap's memory on the glasses."""
        with _c.lock.exclusive():
            if self.loaded:
                _c.lib().sk_unload_bitmap(self._ptr)
            self._ptr = 0

    def __enter__(self) -> Bitmap:
        return self

    def __exit__(self, *exc: object) -> None:
        self.unload()

    def __repr__(self) -> str:
        state = "" if self.loaded else ", unloaded"
        return f"<Bitmap {self.width}x{self.height}{state}>"


def create_bitmap(width: int, height: int) -> Bitmap:
    """A bitmap with all pixels 0."""
    if width <= 0 or height <= 0:
        raise ValueError(f"bad bitmap size {width}x{height}")
    with _c.lock.shared():
        handle = _c.lib().sk_create_bitmap(width, height)
        if not handle:
            raise _no_room(width, height)
        return Bitmap(handle, width, height)


def load_bitmap(image: ImageLike, dither: bool = True, stored: bool = False) -> Bitmap:
    """A bitmap the size of the image, with the image written into it (see Bitmap.write).
    stored: also keep its pixels (as at its first flush) in each arm's flash (VM.md §6.10)."""
    gray = to_gray(image)
    if not gray.width or not gray.height:
        raise ValueError("empty image")
    with _c.lock.shared():
        handle = _c.lib().sk_load_bitmap_keep(gray.tobytes(), gray.width, gray.height, gray.width,
                                              int(dither), int(stored))
        if not handle:
            raise _no_room(gray.width, gray.height)
        return Bitmap(handle, gray.width, gray.height)


def _no_room(width: int, height: int) -> ScouterKitError:
    if _c.session is None:
        return NotInitializedError("scouterkit.init() first")
    if _c.session.keep_sessions:
        return ScouterKitError("a kept session (keep_sessions) owns no memory for bitmaps")
    lib = _c.lib()
    return DeviceMemoryError(f"no room for a {width}x{height} bitmap "
                       f"({lib.sk_free_memory()} of {lib.sk_total_memory()} bytes free)")
