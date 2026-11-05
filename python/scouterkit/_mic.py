from __future__ import annotations

import ctypes
import signal
import threading
from array import array
from collections.abc import Iterator
from dataclasses import dataclass
from enum import IntEnum
from typing import TYPE_CHECKING, Any

from . import _c
from .errors import NotInitializedError, NoReplyError, ScouterKitError

if TYPE_CHECKING:
    from ._arm import Arm

SAMPLE_RATE = 16000
FRAME_SAMPLES = 160


class MicRate(IntEnum):
    """The LC3 bitrate on the link."""

    KBPS_32 = 0  # 40-byte frames
    KBPS_16 = 1  # 20-byte frames


class MicStopReason(IntEnum):
    HOST = 0  # stop(), or never started
    PANEL_OFF = 1  # the panel went off (PANEL = 0)
    FAULT = 2  # a capture fault on the arm
    LINK = 3  # the link dropped


@dataclass(frozen=True)
class MicFrame:
    """10 ms of the microphone, decoded."""

    counter: int  # frames since the start
    concealed: bool  # lost on the link or undecodable: the decoder's concealment
    device_us: int  # the arm's clock when it sent the frame
    host_us: int  # arrival, on now_us()'s clock
    pcm: array  # 160 int16 samples of 16 kHz mono


@dataclass(frozen=True)
class MicState:
    on: bool
    stopped: MicStopReason  # why it last stopped
    frame_bytes: int  # the arm's reply to MIC
    frames_per_event: int
    events: int  # received since the start
    frames: int
    gaps: int  # counter gaps, and the frames they missed (concealed)
    missing: int
    bad: int  # frames the decoder could not decode (concealed)
    dropped: int  # frames not read in time


def mic_state(arm: int) -> MicState:
    s = _c.MicState()
    with _c.lock.shared():
        _c.lib().sk_g1_mic_get_state(arm, ctypes.byref(s))
    return MicState(bool(s.on), MicStopReason(s.stopped), s.frame_bytes, s.frames_per_event,
                    s.events, s.frames, s.gaps, s.missing, s.bad, s.dropped)


# SIGTERM and SIGHUP raise SystemExit while a microphone is on, so that it goes off on the
# way out (the arm keeps capturing until its link drops otherwise).
_active: set[int] = set()
_saved: dict[int, Any] = {}
_guard_lock = threading.Lock()


def _exit_on_signal(signum: int, frame: object) -> None:
    raise SystemExit(128 + signum)


def _guard(arm: int) -> None:
    with _guard_lock:
        _active.add(arm)
        if threading.current_thread() is not threading.main_thread():
            return
        for s in (signal.SIGTERM, signal.SIGHUP):
            if s not in _saved and signal.getsignal(s) is signal.SIG_DFL:
                _saved[s] = signal.signal(s, _exit_on_signal)


def _unguard(*arms: int) -> None:
    with _guard_lock:
        _active.difference_update(arms)
        if _active or threading.current_thread() is not threading.main_thread():
            return
        for s, handler in _saved.items():
            if signal.getsignal(s) is _exit_on_signal:
                signal.signal(s, handler)
        _saved.clear()


class Microphone:
    """The arm's microphone (VM.md §6.5). Only the host switches it on, here with start() or
    a with-block, which also switches it off at its end; deinit() and the interpreter's exit
    switch it off too. While it is on, the arm shows its indicator (a 16 x 16 block in the
    panel's outer top corner; the panel goes on) and streams LC3 frames, which the library
    decodes and conceals when lost. The arm stops by itself when the link drops or its panel
    goes off, and nothing starts it again.

        with scouterkit.right.mic() as mic:
            for frame in mic:          # 10 ms each, until the microphone stops
                use(frame.pcm)
    """

    def __init__(self, arm: Arm, rate: MicRate = MicRate.KBPS_32) -> None:
        self.arm = arm
        self.rate = MicRate(rate)

    def start(self) -> None:
        """MIC 1, once the arm ran what was queued before."""
        with _c.lock.shared():
            if _c.session is None:
                raise NotInitializedError("scouterkit.init() first")
            lib = _c.lib()
            _guard(self.arm.index)
            if not lib.sk_g1_mic_start(self.arm.index, self.rate):
                _unguard(self.arm.index)
                s = _c.State()
                lib.sk_g1_get_state(self.arm.index, ctypes.byref(s))
                why = s.last_error.decode(errors="replace") if s.present else "not configured"
                raise ScouterKitError(f"{self.arm.name} arm: microphone: {why}")

    def stop(self) -> None:
        """MIC 0, and the panel off again if the library expects it off."""
        try:
            with _c.lock.shared():
                stopped = _c.lib().sk_g1_mic_stop(self.arm.index)
            if not stopped and self.state.on:
                raise NoReplyError(f"{self.arm.name} arm: MIC 0")
        finally:
            _unguard(self.arm.index)

    @property
    def state(self) -> MicState:
        return mic_state(self.arm.index)

    def read(self, timeout: float = 1.0) -> MicFrame | None:
        """The oldest frame, or None if none came within timeout seconds. The library keeps
        the last 2 s."""
        f = _c.MicFrame()
        with _c.lock.shared():
            if not _c.lib().sk_g1_mic_read(self.arm.index, ctypes.byref(f), round(timeout * 1000)):
                return None
        return MicFrame(f.counter, bool(f.concealed), f.device_us, f.host_us, array("h", f.pcm))

    def __iter__(self) -> Iterator[MicFrame]:
        """The frames in order, until the microphone is off and every frame is read."""
        while True:
            frame = self.read(0.1)
            if frame is not None:
                yield frame
            elif not self.state.on:
                return

    def __enter__(self) -> Microphone:
        self.start()
        return self

    def __exit__(self, *exc: object) -> None:
        self.stop()
