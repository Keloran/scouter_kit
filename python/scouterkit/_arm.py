from __future__ import annotations

import ctypes
import time
from collections.abc import Iterator
from dataclasses import dataclass
from enum import IntEnum

from PIL import Image

from . import _c
from ._core import ARENA_BYTES, PANEL_HEIGHT, PANEL_WIDTH
from ._mic import Microphone, MicRate, MicState, mic_state
from ._program import Program, Prop, Trigger
from .errors import NoReplyError, ScouterKitError


class EventType(IntEnum):
    ERROR = 0x02  # the arm refused host code
    HALT = 0x03  # a program stopped: HALT or an error
    NOTIFY = 0x04  # NOTIFY by a program or host code
    INPUT = 0x07  # a trigger asked for with report_inputs()
    RESET = 0x80  # the arm lost its session: its program stopped, its memory was restored


@dataclass(frozen=True)
class Event:
    type: EventType | int
    epoch: int
    device_us: int  # the arm's clock when it made the event
    host_us: int  # arrival, on now_us()'s clock
    trigger: int  # HALT: the task's; NOTIFY: its source (0xFF host code); INPUT: which
    count: int  # NOTIFY, INPUT: the arm's counter; ERROR: the packet's seq
    a: int  # NOTIFY: its values; INPUT: a = the value
    b: int
    address: int  # HALT, ERROR: the failing instruction
    op: int  # HALT, ERROR: its opcode
    code: int  # HALT, ERROR: the error code (HALT: 0)
    detail: int
    offset: int  # ERROR: of the instruction in its packet


@dataclass(frozen=True)
class ArmState:
    present: bool  # configured (not "none")
    connected: bool  # link up and session ready
    firmware: str | None
    address: str
    max_packet: int
    interval_us: int  # connection interval
    clock_error_us: int  # bound of the clock estimate
    idle: bool  # the idle link profile is asked for
    queued: int  # commands not sent yet
    packets: int
    resent: int
    reconnects: int
    epoch: int
    epoch_changes: int  # the arm lost its memory (rebooted) and was restored
    errors: int  # commands refused by the library or the arm
    last_error: str
    shown_update: int  # the last flush (1, 2, ...) this eye showed
    shown_us: int
    panel_on: bool  # as the library expects it
    safe_mode: bool
    encrypted: bool
    in_case: bool
    microphone: bool  # MIC captures and streams on this firmware
    triggers: int  # that this firmware can raise (bit i = trigger i)
    events_lost: int


@dataclass(frozen=True)
class Battery:
    millivolts: int | None
    percent: int | None
    charging: bool


@dataclass(frozen=True)
class VMStatus:
    state: int  # 0 stopped, 1 running, 2 halted
    trigger: int  # of the current task, 0xFF none
    pending: int
    table: int
    next: int  # the task's next instruction (halted: the failing one)
    task: int  # 0 none, 1 running, 2 SLEEP_UNTIL, 3 WAIT_EVENT
    jumps_left: int
    depth: int
    halt_code: int
    timer: int
    tasks: int
    coalesced: int
    notify_dropped: int
    cap_pauses: int
    busy_ms: int
    timer_armed: bool

    @property
    def running(self) -> bool:
        return self.state == 1


def now_us() -> int:
    """The library's host clock (steady, µs)."""
    return _c.lib().sk_g1_now_us()


def _crcs() -> tuple[ctypes.c_uint32, ctypes.c_uint32]:
    return ctypes.c_uint32(), ctypes.c_uint32()


class Arm:
    """One eye of the glasses, for the few things that are per arm: scouterkit.left and
    scouterkit.right."""

    def __init__(self, index: int) -> None:
        self.index = index
        self.name = ("left", "right")[index]

    def __repr__(self) -> str:
        return f"scouterkit.{self.name}"

    def state(self) -> ArmState:
        s = _c.State()
        with _c.lock.shared():
            _c.lib().sk_g1_get_state(self.index, ctypes.byref(s))
        firmware = f"{s.firmware_major}.{s.firmware_minor}.{s.firmware_patch}" if s.connected else None
        return ArmState(
            present=bool(s.present), connected=bool(s.connected), firmware=firmware,
            address=s.address.decode(errors="replace"), max_packet=s.max_packet,
            interval_us=s.interval_us, clock_error_us=s.clock_error_us, idle=bool(s.idle),
            queued=s.queued, packets=s.packets, resent=s.resent, reconnects=s.reconnects,
            epoch=s.epoch, epoch_changes=s.epoch_changes, errors=s.errors,
            last_error=s.last_error.decode(errors="replace"), shown_update=s.shown_update,
            shown_us=s.shown_us, panel_on=bool(s.panel_on), safe_mode=bool(s.safe_mode),
            encrypted=bool(s.encrypted), in_case=bool(s.in_case),
            microphone=bool(s.microphone), triggers=s.triggers, events_lost=s.events_lost)

    def _last_error(self) -> str:
        return self.state().last_error

    def set_brightness(self, level: int) -> None:
        """The panel's brightness, 0...42."""
        if not 0 <= level <= 42:
            raise ValueError(f"brightness {level} is not in 0...42")
        with _c.lock.shared():
            if not _c.lib().sk_g1_set_brightness(self.index, level):
                raise NoReplyError(f"{self.name} arm: not connected")

    def set_panel(self, on: bool) -> None:
        """Switches the panel off (its image is lost) or on (all black)."""
        with _c.lock.shared():
            _c.lib().sk_g1_set_panel(self.index, int(on))

    def battery(self) -> Battery:
        """A control request (PROPS): it does not wait for queued commands."""
        mv, percent, charging = ctypes.c_int(), ctypes.c_int(), ctypes.c_int()
        with _c.lock.shared():
            if not _c.lib().sk_g1_battery(self.index, ctypes.byref(mv), ctypes.byref(percent),
                                          ctypes.byref(charging)):
                raise NoReplyError(f"{self.name} arm: BATTERY")
        return Battery(None if mv.value < 0 else mv.value,
                       None if percent.value < 0 else percent.value, bool(charging.value))

    def panel_crc(self, y0: int = 0, rows: int = PANEL_HEIGHT) -> tuple[int, int]:
        """Reads panel rows y0...y0+rows-1 back from the arm as a CRC-32: (read back, what the
        library expects). Waits first until the arm ran every command sent before, however
        long an upload takes while the arm makes progress. NoReplyError after 15 s without
        progress, and while the panel is off."""
        got, want = _crcs()
        with _c.lock.shared():
            if not _c.lib().sk_g1_panel_crc(self.index, y0, rows, ctypes.byref(got),
                                            ctypes.byref(want)):
                raise NoReplyError(f"{self.name} arm: PANEL_READ")
        return got.value, want.value

    def arena_crc(self, address: int = 0, length: int = 0) -> tuple[int, int]:
        """The CRC-32 of arena bytes as the arm holds them and as the library expects them
        (length 0: to the end), after the commands sent before have run. NoReplyError also
        for bytes a loaded program changes."""
        got, want = _crcs()
        with _c.lock.shared():
            if not _c.lib().sk_g1_arena_crc(self.index, address, length, ctypes.byref(got),
                                            ctypes.byref(want)):
                raise NoReplyError(f"{self.name} arm: ARENA_CRC")
        return got.value, want.value

    def read_arena(self, address: int, n: int) -> bytes:
        """n bytes of the arm's arena, after the commands sent before have run."""
        if n <= 0:
            return b""
        out = (ctypes.c_uint8 * n)()
        with _c.lock.shared():
            if not _c.lib().sk_g1_read_arena(self.index, address, out, n):
                raise NoReplyError(f"{self.name} arm: ARENA_CRC (read)")
        return bytes(out)

    def write_arena(self, address: int, data: bytes) -> None:
        """Queues WRITEs of the bytes at address."""
        data = bytes(data)
        if data:
            with _c.lock.shared():
                _c.lib().sk_g1_write_arena(self.index, address, data, len(data))

    def mirror(self, address: int, n: int) -> bytes:
        """n bytes of the library's copy of the arena: what the host wrote there (a font blob,
        a table), not what programs changed. No round trip."""
        with _c.lock.shared():
            _c.require_init()
            if n < 0 or address < 0 or address + n > ARENA_BYTES:
                raise ValueError("outside the arena")
            arena = _c.lib().sk_g1_arena(self.index)
            return ctypes.string_at(ctypes.addressof(arena.contents) + address, n)

    def raise_(self, trigger: Trigger | int) -> None:
        """Sets a trigger pending (USER0...USER3 hand work to the program)."""
        with _c.lock.shared():
            _c.lib().sk_g1_raise(self.index, trigger)

    def notify(self, a: int, b: int) -> None:
        """Queues NOTIFY a, b: an EventType.NOTIFY once the arm ran everything before it."""
        with _c.lock.shared():
            _c.lib().sk_g1_notify(self.index, a, b)

    def sys_write(self, prop: Prop | int, value: int) -> None:
        with _c.lock.shared():
            _c.lib().sk_g1_sys_write(self.index, prop, value & 0xFFFFFFFF)

    def call(self, address: int) -> None:
        """Runs stored code at address as part of a packet (VM.md §6.2)."""
        with _c.lock.shared():
            _c.lib().sk_g1_call(self.index, address)

    def video(self, bitmap: int, stride: int, w: int, h: int, x: int, y: int, level: int,
              src: int, n: int, state: int) -> None:
        """Queues VIDEO: decodes the n-byte record at src into the 1-bpp bitmap and draws the
        rows that changed at (x, y), ink as level (VM.md §3.7)."""
        with _c.lock.shared():
            _c.lib().sk_g1_video(self.index, bitmap, stride, w, h, x, y, level, src, n, state)

    def run(self, code: bytes | Program) -> None:
        """Queues host code (a Program's code without jumps) as one packet."""
        data = code.code if isinstance(code, Program) else bytes(code)
        if data:
            with _c.lock.shared():
                _c.lib().sk_g1_run(self.index, data, len(data))

    def load(self, program: Program) -> None:
        """Queues the program's table, pool and code and marks what it changes."""
        program.validate()
        with _c.lock.shared():
            if not _c.lib().sk_g1_load(self.index, program._ptr):
                raise ScouterKitError(f"{self.name} arm: load refused: {self._last_error()}")

    def start(self, table: int | Program) -> None:
        """Waits until the arm ran everything sent before, then starts the program whose
        trigger table is at table (VM_START)."""
        address = table.table_address if isinstance(table, Program) else table
        with _c.lock.shared():
            if not _c.lib().sk_g1_vm_start(self.index, address):
                raise NoReplyError(f"{self.name} arm: VM_START")

    def stop(self) -> None:
        """Stops the program (VM_STOP)."""
        with _c.lock.shared():
            if not _c.lib().sk_g1_vm_stop(self.index):
                raise NoReplyError(f"{self.name} arm: VM_STOP")

    def vm_status(self) -> VMStatus:
        s = _c.VmStatus()
        with _c.lock.shared():
            if not _c.lib().sk_g1_get_vm_status(self.index, ctypes.byref(s)):
                raise NoReplyError(f"{self.name} arm: VM_STATUS")
        return VMStatus(s.state, s.trigger, s.pending, s.table, s.next, s.task, s.jumps_left,
                        s.depth, s.halt_code, s.timer, s.tasks, s.coalesced, s.notify_dropped,
                        s.cap_pauses, s.busy_ms, bool(s.timer_armed))

    def props(self, *ids: Prop | int) -> list[int]:
        """Up to 12 properties (UNKNOWN for values the arm does not have). A control request:
        it does not wait for queued commands (notify() and its event do)."""
        if not 1 <= len(ids) <= 12:
            raise ValueError(f"{len(ids)} properties (1...12 per request)")
        request = (ctypes.c_uint8 * len(ids))(*ids)
        values = (ctypes.c_uint32 * len(ids))()
        with _c.lock.shared():
            if not _c.lib().sk_g1_props(self.index, request, len(ids), values):
                raise NoReplyError(f"{self.name} arm: PROPS")
        return list(values)

    def prop(self, prop: Prop | int) -> int:
        return self.props(prop)[0]

    def report_inputs(self, triggers: int) -> int:
        """Asks the arm to report these triggers (bit i = trigger i) as EventType.INPUT, also
        after reconnecting. Returns the mask in force: the request and what this firmware can
        raise."""
        in_force = ctypes.c_uint16()
        with _c.lock.shared():
            if not _c.lib().sk_g1_report_inputs(self.index, triggers, ctypes.byref(in_force)):
                raise NoReplyError(f"{self.name} arm: EVENTS")
        return in_force.value

    def next_event(self, timeout: float = 0.0) -> Event | None:
        """The arm's oldest event, waiting up to timeout seconds; None if none came. The
        library keeps the last 256."""
        deadline = time.monotonic() + timeout
        e = _c.Event()
        while True:
            wait = min(max(deadline - time.monotonic(), 0.0), 0.1)
            with _c.lock.shared():
                got = _c.lib().sk_g1_next_event(self.index, ctypes.byref(e), round(wait * 1000))
            if got:
                try:
                    kind: EventType | int = EventType(e.type)
                except ValueError:
                    kind = e.type
                return Event(kind, e.epoch, e.device_us, e.host_us, e.trigger, e.count, e.a,
                             e.b, e.address, e.op, e.code, e.detail, e.offset)
            if time.monotonic() >= deadline:
                return None

    def events(self, timeout: float | None = None) -> Iterator[Event]:
        """The arm's events as they come; with a timeout, until none came for that long."""
        while True:
            e = self.next_event(3600.0 if timeout is None else timeout)
            if e is not None:
                yield e
            elif timeout is not None:
                return

    def device_time(self, after: float = 0.0) -> int:
        """The arm's clock (µs, wraps) at now_us() + after seconds."""
        with _c.lock.shared():
            return _c.lib().sk_g1_device_time(self.index, round(after * 1e6))

    def disconnect(self, seconds: float) -> None:
        """Drops the link; the library connects again after that long. The session, the arm's
        memory and its program stay."""
        with _c.lock.shared():
            _c.lib().sk_g1_disconnect(self.index, round(seconds * 1000))

    def mic(self, rate: MicRate = MicRate.KBPS_32) -> Microphone:
        """The arm's microphone (on the G1 the right arm, state().microphone): on while a
        with-block runs, see Microphone."""
        return Microphone(self, rate)

    def mic_state(self) -> MicState:
        return mic_state(self.index)

    def emulated_panel(self) -> Image.Image:
        """What the emulated arm's panel shows (640 x 480, level x 17; black while it is off),
        after the arm ran every command sent before. Emulator only."""
        _require_emulator()
        with _c.lock.exclusive():
            lib = _c.lib()
            got, want = _crcs()
            lib.sk_g1_panel_crc(self.index, 0, 1, ctypes.byref(got), ctypes.byref(want))
            data = ctypes.string_at(lib.sk_g1_emulator_panel(self.index), PANEL_WIDTH * PANEL_HEIGHT)
        return Image.frombytes("L", (PANEL_WIDTH, PANEL_HEIGHT), data.translate(_LEVELS))

    def set_emulated_property(self, prop: Prop | int, value: int) -> None:
        """A sensor value of the emulated arm (battery, case, ...); BATT_PCT follows it with
        the firmware's 30 s hysteresis. Emulator only."""
        _require_emulator()
        with _c.lock.shared():
            _c.lib().sk_g1_emulator_set_property(self.index, prop, value & 0xFFFFFFFF)

    def set_emulated_mic(self, hz: float, peak: float) -> None:
        """What the emulated arm's microphone hears: a sine of hz, its peak at peak x full
        scale (440 Hz, 0.25 at first; the right arm has the microphone). Emulator only."""
        _require_emulator()
        with _c.lock.shared():
            _c.lib().sk_g1_emulator_set_mic(self.index, hz, peak)


_LEVELS = bytes(min(255, 17 * v) for v in range(256))


def _require_emulator() -> None:
    if not _c.is_emulator():
        raise ScouterKitError("an emulator call, but the glasses library is loaded")


def emulate_new_host(keep_sessions: bool) -> None:
    """The emulated arms see a new host: the library forgets their sessions and connects
    again, as a new process with init(keep_sessions=...) would. Emulator only."""
    _require_emulator()
    with _c.lock.shared():
        _c.lib().sk_g1_emulator_new_host(int(keep_sessions))


left = Arm(0)
right = Arm(1)
