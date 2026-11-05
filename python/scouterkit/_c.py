"""ctypes declarations of the C layer (../src/c/scouter_kit_c.h) and the shared state."""

from __future__ import annotations

import ctypes
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from ctypes import (POINTER, Structure, c_char, c_char_p, c_double, c_int, c_int16, c_int64,
                    c_size_t, c_uint8, c_uint16, c_uint32, c_uint64, c_void_p)
from pathlib import Path

from .errors import NotInitializedError, ScouterKitError

LIBRARY = "libscouterkit.so"
EMULATOR_LIBRARY = "libscouterkit_emulator.so"


class View(Structure):
    _fields_ = [("data", POINTER(c_uint8)), ("width", c_int), ("height", c_int), ("stride", c_int)]


class Config(Structure):
    _fields_ = [("dx", c_int), ("dy", c_int), ("left", c_char_p), ("right", c_char_p),
                ("adapter", c_char_p), ("timeout_ms", c_int), ("keep_sessions", c_int),
                ("log", c_int)]


class VmStatus(Structure):
    _fields_ = [("state", c_int), ("trigger", c_int), ("pending", c_uint16), ("table", c_uint32),
                ("next", c_uint32), ("task", c_int), ("jumps_left", c_int), ("depth", c_int),
                ("halt_code", c_int), ("timer", c_uint32), ("tasks", c_uint32), ("coalesced", c_int),
                ("notify_dropped", c_int), ("cap_pauses", c_int), ("busy_ms", c_int),
                ("timer_armed", c_int)]


class Event(Structure):
    _fields_ = [("type", c_int), ("epoch", c_uint32), ("device_us", c_uint32), ("host_us", c_int64),
                ("trigger", c_int), ("count", c_uint32), ("a", c_uint32), ("b", c_uint32),
                ("address", c_uint32), ("op", c_int), ("code", c_int), ("detail", c_uint32),
                ("offset", c_int)]


class State(Structure):
    _fields_ = [("present", c_int), ("connected", c_int), ("firmware_major", c_int),
                ("firmware_minor", c_int), ("firmware_patch", c_int), ("epoch", c_uint32),
                ("max_packet", c_int), ("interval_us", c_int), ("clock_error_us", c_int),
                ("idle", c_int), ("queued", c_int), ("packets", c_uint32), ("resent", c_uint32),
                ("reconnects", c_uint32), ("epoch_changes", c_uint32), ("errors", c_uint32),
                ("shown_update", c_uint64), ("shown_us", c_int64), ("panel_on", c_int),
                ("safe_mode", c_int), ("encrypted", c_int), ("in_case", c_int),
                ("microphone", c_int), ("triggers", c_uint16), ("events_lost", c_uint32),
                ("last_error", c_char * 160), ("address", c_char * 40)]


class MicFrame(Structure):
    _fields_ = [("counter", c_uint32), ("concealed", c_int), ("device_us", c_uint32),
                ("host_us", c_int64), ("pcm", c_int16 * 160)]


class MicState(Structure):
    _fields_ = [("on", c_int), ("stopped", c_int), ("frame_bytes", c_int),
                ("frames_per_event", c_int), ("events", c_uint64), ("frames", c_uint64),
                ("gaps", c_uint64), ("missing", c_uint64), ("bad", c_uint64), ("dropped", c_uint64)]


class Asset(Structure):
    _fields_ = [("id", c_uint8 * 16), ("offset", c_uint32), ("size", c_uint32), ("crc", c_uint32),
                ("type", c_uint8)]


u8p = POINTER(c_uint8)
u32p = POINTER(c_uint32)
intp = POINTER(c_int)
vp = c_void_p

FUNCTIONS = {
    "sk_load_font": (vp, [c_char_p, c_size_t, c_char_p, c_int, c_char_p, c_size_t]),
    "sk_load_font_keep": (vp, [c_char_p, c_size_t, c_char_p, c_int, c_int, c_char_p, c_size_t]),
    "sk_font_metrics": (None, [vp, intp, intp, intp]),
    "sk_font_measure": (None, [vp, c_char_p, intp, intp, intp, intp]),
    "sk_font_draw_line": (c_int, [vp, vp, c_char_p, c_int, c_int, c_int, c_uint8]),
    "sk_unload_font": (None, [vp]),
    "sk_load_bitmap": (vp, [c_char_p, c_int, c_int, c_int, c_int]),
    "sk_load_bitmap_keep": (vp, [c_char_p, c_int, c_int, c_int, c_int, c_int]),
    "sk_create_bitmap": (vp, [c_int, c_int]),
    "sk_bitmap_contents": (View, [vp]),
    "sk_write": (None, [vp, c_char_p, c_int, c_int, c_int, c_int, c_int, c_int]),
    "sk_fill": (None, [vp, c_int, c_int, c_int, c_int, c_uint8]),
    "sk_draw": (None, [vp, c_int, c_int]),
    "sk_clear": (None, []),
    "sk_unload_bitmap": (None, [vp]),
    "sk_display": (View, []),
    "sk_response": (u8p, []),
    "sk_unload_all": (None, []),
    "sk_total_memory": (c_int, []),
    "sk_free_memory": (c_int, []),
    "sk_flush": (None, []),
    "sk_g1_init_config": (c_int, [POINTER(Config), c_char_p, c_size_t]),
    "sk_g1_deinit": (None, []),
    "sk_g1_now_us": (c_int64, []),
    "sk_g1_write_arena": (None, [c_int, c_uint32, c_char_p, c_size_t]),
    "sk_g1_set_panel": (None, [c_int, c_int]),
    "sk_g1_sys_write": (None, [c_int, c_int, c_uint32]),
    "sk_g1_notify": (None, [c_int, c_uint32, c_uint32]),
    "sk_g1_raise": (None, [c_int, c_int]),
    "sk_g1_call": (None, [c_int, c_uint32]),
    "sk_g1_video": (None, [c_int, c_uint32, c_uint32, c_uint32, c_uint32, c_int, c_int, c_uint8,
                           c_uint32, c_uint32, c_uint32]),
    "sk_g1_video_state": (c_uint32, []),
    "sk_g1_video_encoder_new": (vp, [c_int, c_int]),
    "sk_g1_video_encoder_free": (None, [vp]),
    "sk_g1_video_encode": (u8p, [vp, c_char_p, c_int, c_size_t, POINTER(c_size_t)]),
    "sk_g1_run": (None, [c_int, c_char_p, c_size_t]),
    "sk_g1_program_new": (vp, [c_uint32, c_uint32, c_int, c_uint32]),
    "sk_g1_program_free": (None, [vp]),
    "sk_g1_program_label": (c_int, [vp]),
    "sk_g1_program_label_at": (c_int, [vp, c_uint32]),
    "sk_g1_program_bind": (None, [vp, c_int]),
    "sk_g1_program_on": (None, [vp, c_int, c_int]),
    "sk_g1_program_const": (c_uint16, [vp, c_uint32]),
    "sk_g1_program_changes": (None, [vp, c_uint32, c_uint32]),
    "sk_g1_program_here": (c_uint32, [vp]),
    "sk_g1_program_op": (None, [vp, c_int, POINTER(c_uint16), c_int, c_int]),
    "sk_g1_program_write": (None, [vp, c_uint32, c_char_p, c_size_t]),
    "sk_g1_program_write_rle": (None, [vp, c_uint32, c_uint16, c_char_p, c_size_t]),
    "sk_g1_program_valid": (c_int, [vp, c_char_p, c_size_t]),
    "sk_g1_program_code": (u8p, [vp, POINTER(c_size_t)]),
    "sk_g1_program_pool": (u32p, [vp, POINTER(c_size_t)]),
    "sk_g1_program_table": (u32p, [vp]),
    "sk_g1_load": (c_int, [c_int, vp]),
    "sk_g1_vm_start": (c_int, [c_int, c_uint32]),
    "sk_g1_vm_stop": (c_int, [c_int]),
    "sk_g1_get_vm_status": (c_int, [c_int, POINTER(VmStatus)]),
    "sk_g1_props": (c_int, [c_int, u8p, c_int, u32p]),
    "sk_g1_report_inputs": (c_int, [c_int, c_uint16, POINTER(c_uint16)]),
    "sk_g1_read_arena": (c_int, [c_int, c_uint32, u8p, c_size_t]),
    "sk_g1_disconnect": (None, [c_int, c_int]),
    "sk_g1_next_event": (c_int, [c_int, POINTER(Event), c_int]),
    "sk_g1_alloc": (c_int64, [c_uint32]),
    "sk_g1_free": (None, [c_uint32]),
    "sk_g1_bitmap_address": (c_uint32, [vp, intp]),
    "sk_g1_font_address": (c_uint32, [vp]),
    "sk_g1_glyph_indices": (c_size_t, [vp, c_char_p, u32p, c_size_t]),
    "sk_g1_device_time": (c_uint32, [c_int, c_int64]),
    "sk_g1_arena": (u32p, [c_int]),
    "sk_g1_get_state": (None, [c_int, POINTER(State)]),
    "sk_g1_set_brightness": (c_int, [c_int, c_int]),
    "sk_g1_battery": (c_int, [c_int, intp, intp, intp]),
    "sk_g1_panel_crc": (c_int, [c_int, c_int, c_int, u32p, u32p]),
    "sk_g1_arena_crc": (c_int, [c_int, c_uint32, c_uint32, u32p, u32p]),
    "sk_g1_mic_start": (c_int, [c_int, c_int]),
    "sk_g1_mic_stop": (c_int, [c_int]),
    "sk_g1_mic_read": (c_int, [c_int, POINTER(MicFrame), c_int]),
    "sk_g1_mic_get_state": (None, [c_int, POINTER(MicState)]),
    "sk_py_departure_mono": (u8p, [POINTER(c_size_t)]),
    "sk_g1_asset_id": (None, [c_char_p, c_size_t, u8p]),
    "sk_g1_store": (c_int, [c_char_p, c_size_t, c_int, c_char_p, c_size_t]),
    "sk_g1_forget": (c_int, [c_char_p, c_char_p, c_size_t]),
    "sk_g1_assets": (c_int, [c_int, POINTER(Asset), c_int]),
}

EMULATOR_FUNCTIONS = {
    "sk_g1_emulator_panel": (u8p, [c_int]),
    "sk_g1_emulator_set_property": (None, [c_int, c_int, c_uint32]),
    "sk_g1_emulator_set_mic": (None, [c_int, c_double, c_double]),
    "sk_g1_emulator_new_host": (None, [c_int]),
    "sk_g1_emulator_set_asset_store": (None, [c_int, c_int]),
}

_lib: ctypes.CDLL | None = None
_load_lock = threading.Lock()


def _open(emulator: bool) -> ctypes.CDLL:
    path = str(Path(__file__).with_name(EMULATOR_LIBRARY if emulator else LIBRARY))
    try:
        lib = ctypes.CDLL(path)
    except OSError as e:
        raise ScouterKitError(f"{e} (build it with python/build.sh)") from e
    functions = dict(FUNCTIONS)
    if hasattr(lib, "sk_g1_emulator_panel"):
        functions.update(EMULATOR_FUNCTIONS)
    for name, (restype, argtypes) in functions.items():
        f = getattr(lib, name)
        f.restype, f.argtypes = restype, argtypes
    return lib


def load(emulator: bool | None = None) -> ctypes.CDLL:
    """Loads the library once: the emulator's if asked, else the glasses' (None: the one
    already loaded, else the glasses')."""
    global _lib
    with _load_lock:
        if _lib is None:
            _lib = _open(bool(emulator))
        if emulator is not None and emulator != is_emulator(_lib):
            loaded = "emulator" if is_emulator(_lib) else "glasses"
            raise ScouterKitError(f"the {loaded} library is already loaded in this process")
        return _lib


def lib() -> ctypes.CDLL:
    return _lib or load()


def is_emulator(library: ctypes.CDLL | None = None) -> bool:
    return hasattr(library or lib(), "sk_g1_emulator_panel")


class RWLock:
    """Many holders of shared(), or one of exclusive()."""

    def __init__(self) -> None:
        self._cond = threading.Condition()
        self._readers = 0
        self._writer = False

    @contextmanager
    def shared(self) -> Iterator[None]:
        with self._cond:
            while self._writer:
                self._cond.wait()
            self._readers += 1
        try:
            yield
        finally:
            with self._cond:
                self._readers -= 1
                if not self._readers:
                    self._cond.notify_all()

    @contextmanager
    def exclusive(self) -> Iterator[None]:
        with self._cond:
            while self._writer or self._readers:
                self._cond.wait()
            self._writer = True
        try:
            yield
        finally:
            with self._cond:
                self._writer = False
                self._cond.notify_all()


# Calls that use a handle hold the lock shared; calls that free handles or copy
# pixels out of the library hold it exclusive. generation changes whenever all
# handles become invalid.
lock = RWLock()
generation = 0
session = None


def require_init() -> None:
    if session is None:
        raise NotInitializedError("scouterkit.init() first")


def view_bytes(v: View) -> bytes:
    if not v.data or v.width <= 0 or v.height <= 0:
        return b""
    raw = ctypes.string_at(v.data, (v.height - 1) * v.stride + v.width)
    if v.stride == v.width:
        return raw
    return b"".join(raw[y * v.stride:y * v.stride + v.width] for y in range(v.height))
