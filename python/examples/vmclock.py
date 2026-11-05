"""Runs a clock as a program on both arms (VM.md §9.2, made with the program builder): each
arm redraws HH:MM every minute by itself, in its own clock. With --away the host drops both
links for that long, then reconnects, reads the clock back from both arms (its digits and
the panel rows) and corrects it, timing the redraw.

    python examples/vmclock.py [--away SECONDS] [--stop] [--emulator]
"""

from __future__ import annotations

import struct
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timedelta

import common
import scouterkit as sk

BAND_W, BAND_H = 160, 32
STRIDE = BAND_W // 2
BAND_X, BAND_Y = 290, 8  # on the virtual display


@dataclass(frozen=True)
class Layout:
    """From base: table, pool, code; then the program's data."""

    base: int

    @property
    def text(self) -> int:
        return self.base + 768

    @property
    def mailbox(self) -> int:
        return self.base + 776

    @property
    def state(self) -> int:
        return self.base + 784

    @property
    def band(self) -> int:
        return self.base + 1024


def program(l: Layout, font: int, x: int, y: int) -> sk.Program:
    """VM.md §9.2 at this layout, with this eye's DRAW position and a NOTIFY [M], [N] after
    each redraw."""
    p = sk.Program(l.base, 48)
    c, r = p.const, sk.R
    m, n = l.state // 4, l.state // 4 + 1
    m_new, n_new = l.mailbox // 4, l.mailbox // 4 + 1
    sync, tick, show = p.label(), p.label(), p.label()
    p.bind(sync)
    p.mov(m, m_new)
    p.mov(n, n_new)
    p.jmp(show)
    p.bind(tick)
    p.add(m, m, c(1))
    p.modu(m, m, c(1440))
    p.add(n, n, c(60_000_000))
    p.bind(show)
    p.divu(r(4), m, c(60))
    p.modu(r(5), m, c(60))
    for op, value, index in ((p.divu, r(4), 0), (p.modu, r(4), 1), (p.divu, r(5), 3), (p.modu, r(5), 4)):
        op(r(6), value, c(10))
        p.store(8, c(l.text), c(index), r(6))
    p.fill(c(l.band), c(STRIDE), c(0), c(0), c(BAND_W), c(BAND_H), c(0))
    p.glyphs(c(font), c(l.band), c(STRIDE), c(4), c(24), c(sk.Mode.MASK), c(15), c(l.text), c(5))
    p.draw(c(l.band), c(STRIDE), c(0), c(0), c(x), c(y), c(BAND_W), c(BAND_H))
    p.sys_write(c(sk.Prop.TIMER), n)
    p.notify(m, n)
    p.end()
    p.on(sk.Trigger.START, sync)
    p.on(sk.Trigger.TIMER, tick)
    p.on(sk.Trigger.USER0, sync)
    p.changes(l.text, 24)
    p.changes(l.band, STRIDE * BAND_H)
    return p


def set_time(arm: sk.Arm, l: Layout) -> None:
    """Writes the minute of the day and the next minute's start, in the arm's clock, to the
    program's mailbox."""
    now = datetime.now()
    next_minute = now.replace(second=0, microsecond=0) + timedelta(minutes=1)
    start = arm.device_time((next_minute - now).total_seconds())
    arm.write_arena(l.mailbox, common.words(now.hour * 60 + now.minute, start))


def wait_redraw(arm: sk.Arm, timeout: float) -> sk.Event:
    """The program's NOTIFY after a redraw."""
    while True:
        e = common.wait_notify(arm, timeout)
        if e.trigger != 0xFF:
            return e


def check(arm: sk.Arm, l: Layout, font: sk.Font, x: int, y: int) -> None:
    """Reads the clock back from the arm: its digits, its state, and the band's panel rows
    against the text drawn here. Close to a minute's start (the arms redraw then), it waits
    until just after it."""
    now = datetime.now()
    if (to_next := 60 - now.second - now.microsecond / 1e6) < 2:
        time.sleep(to_next + 0.5)
    digits = arm.read_arena(l.text, 5)
    minute = struct.unpack("<I", arm.read_arena(l.state, 4))[0]
    text = f"{digits[0]}{digits[1]}:{digits[3]}{digits[4]}"
    if text != f"{minute // 60:02d}:{minute % 60:02d}":
        raise RuntimeError(f"{arm.name} arm: the text says {text}, the state minute {minute}")
    now = datetime.now()
    if all(t.hour * 60 + t.minute != minute for t in (now, now - timedelta(seconds=3), now + timedelta(seconds=3))):
        raise RuntimeError(f"{arm.name} arm shows {text} at {now:%H:%M:%S}")
    with sk.create_bitmap(BAND_W, BAND_H) as band:
        font.draw_line(band, text, 4, 24)
        common.check_panel(arm, band.image(), x, y)
    status = arm.vm_status()
    if not status.running:
        raise RuntimeError(f"{arm.name} arm: program state {status}")
    print(f"{arm.name} arm: {text}, panel rows as drawn here, {status.tasks} tasks, "
          f"timer armed {status.timer_armed}")


def away(arms: list[sk.Arm], seconds: float) -> None:
    print(f"dropping the links for {seconds:g} s (until "
          f"{datetime.now() + timedelta(seconds=seconds):%H:%M:%S})")
    for arm in arms:
        arm.disconnect(seconds)
    time.sleep(seconds)
    deadline = time.monotonic() + 120
    for arm in arms:
        while not arm.state().connected and time.monotonic() < deadline:
            time.sleep(0.1)
        s = arm.state()
        if not s.connected:
            raise RuntimeError(f"{arm.name} arm: not connected again")
        if s.epoch_changes:
            raise RuntimeError(f"{arm.name} arm: it rebooted while the host was away")
    print("connected again")


def main(argv: list[str] | None = None) -> int:
    p = common.parser(__doc__.splitlines()[0])
    p.add_argument("--away", type=float, default=0, metavar="SECONDS",
                   help="drop the links for this long, then check and correct the clock")
    p.add_argument("--stop", action="store_true",
                   help="stop the programs and switch the panels off at the end")
    args = p.parse_args(argv)

    with common.connect(args) as session:
        font = sk.load_font(sk.departure_mono(), "0123456789:", 2)
        l = Layout(sk.alloc(4096))
        colon = font.glyph_indices(":")[0]
        arms = common.present()
        where = {}
        for arm in arms:
            x, y = common.panel_at(session, arm, BAND_X, BAND_Y)
            where[arm] = x, y
            clock = program(l, font.address, x, y)
            arm.write_arena(l.text, bytes([0, 0, colon, 0, 0]))
            set_time(arm, l)
            arm.load(clock)
            arm.start(clock)
            print(f"{arm.name} arm: {len(clock.code)} bytes of code at 0x{clock.code_address:05x}, "
                  f"drawn at panel ({x}, {y})")
        for arm in arms:
            wait_redraw(arm, 5)
            check(arm, l, font, *where[arm])
        if args.away:
            away(arms, args.away)
            for arm in arms:
                check(arm, l, font, *where[arm])
            for arm in arms:
                common.drain(arm)
                set_time(arm, l)
                sent = sk.now_us()
                arm.raise_(sk.Trigger.USER0)
                e = wait_redraw(arm, 5)
                print(f"{arm.name} arm: corrected, redrawn {(e.host_us - sent) / 1000:.0f} ms after the request")
                check(arm, l, font, *where[arm])
        if args.stop:
            for arm in arms:
                arm.stop()
                arm.set_panel(False)
            print("programs stopped, panels off")
        else:
            print("the clock keeps running on the arms")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, TimeoutError, sk.ScouterKitError) as e:
        sys.exit(f"vmclock: {e}")
