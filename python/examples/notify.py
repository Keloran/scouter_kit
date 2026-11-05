"""Shows a message on the glasses now, like `g1 notify`: through a running `g1 mirror` (its
Unix socket), or without one by connecting (3-5 s), loading the notification program on both
arms and leaving it running. The message pops up on both eyes at the same moment and hides by
itself; while the head is tilted up the arms show the last ones again.

    python examples/notify.py [--app NAME] [--timeout 5] [--direct] [--check] [--emulator] TITLE [BODY]
"""

from __future__ import annotations

import json
import os
import socket
import sys
import time
from dataclasses import dataclass
from datetime import datetime

import common
import scouterkit as sk

# The arms keep the last notifications in a ring of slots, each a title and two body lines of
# glyph varints (u8 byte count first).
SLOTS = 8
ENTRY_LINES = 3
LIST_SHOWN = 4  # entries in the list, two lines each
LINE_BYTES = 64
POOL_WORDS = 96
CODE_BYTES = 3072
STALE_US = 2_000_000  # a popup this late shows only in the list
LEVELS = bytes([15, 9, 9])

# View states, NOTIFYd as (state, head) after each change.
HIDDEN, POPUP, LIST, HOST_LIST, HOLD, DISMISSED = range(6)
# State words; the host writes HEAD...TIMEOUT.
W_STATE, W_HIDE_AT, W_HEAD, W_COUNT, W_SHOW_AT, W_TIMEOUT, STATE_WORDS = range(7)


@dataclass(frozen=True)
class Layout:
    base: int

    @property
    def state(self) -> int:
        return self.base + CODE_BYTES

    @property
    def levels(self) -> int:
        return self.state + 4 * STATE_WORDS

    @property
    def text(self) -> int:
        return self.levels + 4

    @property
    def band(self) -> int:
        return self.text + SLOTS * ENTRY_LINES * LINE_BYTES

    def word(self, i: int) -> sk.Word:
        return self.state // 4 + i


@dataclass(frozen=True)
class Face:
    """The views on one arm: the popup at panel (x, y), the list's entries below each other
    from there, slot_h rows each."""

    font: int
    w: int
    x: int
    y: int
    pad: int
    baseline: int
    spacing: int
    popup_h: int
    slot_h: int
    angle: int
    hysteresis: int
    hold_us: int
    list_for_us: int

    @property
    def stride(self) -> int:
        return (self.w + 1) // 2


def program(l: Layout, f: Face) -> sk.Program:
    """Shows notifications from the ring. USER0 (the host wrote a new one) pops it up at
    SHOW_AT for TIMEOUT, or redraws the list while it is shown; TILT shows the list while the
    head is up (above angle) and hides it hold after it goes down; the list also hides after
    list_for, and comes back only once the head has been down. USER1 shows the list for the
    host, USER2 hides everything. TIMER ends a popup, the list or the hold."""
    p = sk.Program(l.base, POOL_WORDS)
    c, r = p.const, sk.R
    state, hide_at, head, count, show_at, timeout = (l.word(i) for i in range(W_TIMEOUT + 1))
    stride, band = c(f.stride), c(l.band)
    lo, hi, timer = c(sk.Prop.PITCH_LO), c(sk.Prop.PITCH_HI), c(sk.Prop.TIMER)

    def now(d: sk.Word) -> None:
        p.sys_read(d, c(sk.Prop.TIME_US))

    def report() -> None:
        p.notify(state, head)

    def down() -> None:  # the TILT range while the list is not up: fires when the head comes up
        p.sys_write(lo, c(-90))
        p.sys_write(hi, c(f.angle))

    def up() -> None:  # while it is up: fires when the head goes down
        p.sys_write(lo, c(f.angle - f.hysteresis))
        p.sys_write(hi, c(90))

    def hide_after(us: int) -> None:
        now(r(1))
        p.add(hide_at, r(1), c(us))
        p.sys_write(timer, hide_at)

    # entry draws r2 lines of slot r1 into the band and shows r4 rows of it at panel row r3;
    # r5 = 1 waits for SHOW_AT first.
    entry, line, skip, drawn, at = (p.label() for _ in range(5))
    p.bind(entry)
    p.fill(band, stride, c(0), c(0), c(f.w), r(4), c(0))
    p.mov(r(6), c(0))
    p.bind(line)
    p.bgeu(r(6), r(2), drawn)
    p.mul(r(7), r(1), c(ENTRY_LINES))
    p.add(r(7), r(7), r(6))
    p.mul(r(7), r(7), c(LINE_BYTES))
    p.add(r(7), r(7), c(l.text))
    p.load(8, r(8), r(7), c(0))
    p.beq(r(8), c(0), skip)
    p.add(r(7), r(7), c(1))
    p.load(8, r(9), c(l.levels), r(6))
    p.mul(r(10), r(6), c(f.spacing))
    p.add(r(10), r(10), c(f.baseline))
    p.glyphs(c(f.font), band, stride, c(f.pad), r(10), c(sk.Mode.MASK), r(9), r(7), r(8))
    p.bind(skip)
    p.add(r(6), r(6), c(1))
    p.jmp(line)
    p.bind(drawn)
    p.beq(r(5), c(0), at)
    p.sleep_until(show_at)
    p.bind(at)
    p.draw(band, stride, c(0), c(0), c(f.x), r(3), c(f.w), r(4))
    p.ret()

    popup = p.label()
    p.bind(popup)
    p.modu(r(1), head, c(SLOTS))
    p.mov(r(2), c(ENTRY_LINES))
    p.mov(r(3), c(f.y))
    p.mov(r(4), c(f.popup_h))
    p.mov(r(5), c(1))
    p.call(entry)
    p.ret()

    listing, following, blank = p.label(), p.label(), p.label()
    p.bind(listing)
    p.mov(r(11), c(0))
    p.bind(following)
    p.mul(r(3), r(11), c(f.slot_h))
    p.add(r(3), r(3), c(f.y))
    p.mov(r(4), c(f.slot_h))
    p.mov(r(5), c(0))
    p.mov(r(2), c(0))
    p.bgeu(r(11), count, blank)
    p.sub(r(1), head, r(11))
    p.modu(r(1), r(1), c(SLOTS))
    p.mov(r(2), c(2))
    p.bind(blank)
    p.call(entry)
    p.add(r(11), r(11), c(1))
    p.bne(r(11), c(LIST_SHOWN), following)
    p.ret()

    hide, show_list = p.label(), p.label()
    p.bind(hide)
    p.sys_write(c(sk.Prop.PANEL), c(0))
    p.mov(state, c(HIDDEN))
    down()
    report()
    p.ret()
    p.bind(show_list)
    p.call(listing)
    p.mov(state, c(LIST))
    up()
    hide_after(f.list_for_us)
    report()
    p.ret()

    start, done = p.label(), p.label()
    p.bind(start)
    p.call(hide)
    p.bind(done)
    p.end()

    fresh, relist, kept = p.label(), p.label(), p.label()
    p.bind(fresh)
    now(r(1))
    p.sub(r(2), r(1), show_at)
    p.blts(c(STALE_US), r(2), done)
    p.beq(state, c(LIST), relist)
    p.beq(state, c(HOST_LIST), relist)
    p.beq(state, c(HOLD), relist)
    p.call(popup)
    p.mov(state, c(POPUP))
    down()
    p.add(hide_at, show_at, timeout)
    p.sys_write(timer, hide_at)
    report()
    p.end()
    p.bind(relist)
    p.call(listing)
    p.beq(state, c(HOLD), kept)
    hide_after(f.list_for_us)
    p.bind(kept)
    report()
    p.end()

    tilt, from_list, from_hold, from_dismissed, still_up, still_down = (p.label() for _ in range(6))
    p.bind(tilt)
    p.sys_read(r(0), c(sk.Prop.PITCH))
    p.beq(r(0), c(sk.UNKNOWN), done)
    p.beq(state, c(LIST), from_list)
    p.beq(state, c(HOLD), from_hold)
    p.beq(state, c(DISMISSED), from_dismissed)
    p.beq(state, c(HOST_LIST), done)
    p.blts(r(0), c(f.angle), still_down)
    p.call(show_list)
    p.end()
    p.bind(still_down)
    down()
    p.end()
    p.bind(from_list)
    p.bges(r(0), c(f.angle - f.hysteresis), still_up)
    if f.hold_us:
        p.mov(state, c(HOLD))
        down()
        hide_after(f.hold_us)
        report()
    else:
        p.call(hide)
    p.end()
    p.bind(still_up)
    up()
    p.end()
    p.bind(from_hold)
    p.blts(r(0), c(f.angle), still_down)
    p.mov(state, c(LIST))
    up()
    hide_after(f.list_for_us)
    report()
    p.end()
    p.bind(from_dismissed)
    p.bges(r(0), c(f.angle - f.hysteresis), still_up)
    p.call(hide)
    p.end()

    tick, early, dismiss = p.label(), p.label(), p.label()
    p.bind(tick)
    now(r(1))
    p.sub(r(2), r(1), hide_at)
    p.blts(r(2), c(0), early)
    p.beq(state, c(LIST), dismiss)
    p.beq(state, c(HIDDEN), done)
    p.beq(state, c(DISMISSED), done)
    p.call(hide)
    p.end()
    p.bind(dismiss)
    p.sys_write(c(sk.Prop.PANEL), c(0))
    p.mov(state, c(DISMISSED))
    report()
    p.end()
    p.bind(early)
    p.beq(state, c(HIDDEN), done)
    p.beq(state, c(DISMISSED), done)
    p.sys_write(timer, hide_at)
    p.end()

    host_list, host_hide = p.label(), p.label()
    p.bind(host_list)
    p.call(listing)
    p.mov(state, c(HOST_LIST))
    p.sys_write(lo, c(-90))
    p.sys_write(hi, c(90))
    hide_after(f.list_for_us)
    report()
    p.end()
    p.bind(host_hide)
    p.call(hide)
    p.end()

    p.on(sk.Trigger.START, start)
    p.on(sk.Trigger.USER0, fresh)
    p.on(sk.Trigger.TILT, tilt)
    p.on(sk.Trigger.TIMER, tick)
    p.on(sk.Trigger.USER1, host_list)
    p.on(sk.Trigger.USER2, host_hide)
    p.changes(l.state, 4 * W_HEAD)
    p.changes(l.band, f.stride * (f.popup_h + f.spacing))
    return p


class Glasses:
    """The notification program on the arms and what the host knows of their ring."""

    def __init__(self, session: sk.Session, arms: list[sk.Arm], glyphs: str, angle: int = 20,
                 hysteresis: int = 5, hold: float = 1.0, list_for: float = 15.0) -> None:
        if not arms:
            raise RuntimeError("no arm")
        self.arms = arms
        self.font = font = sk.load_font(sk.departure_mono(), glyphs, 2)
        pad = 8
        w = (session.size[0] - 2 * abs(session.dx)) | 1
        popup_h = ENTRY_LINES * font.line_spacing + 2 * pad
        baseline = pad + (font.line_spacing - font.ascent - font.descent) // 2 + font.ascent
        text_bytes = SLOTS * ENTRY_LINES * LINE_BYTES
        band_bytes = (w + 1) // 2 * (popup_h + font.line_spacing)
        self.layout = l = Layout(sk.alloc(CODE_BYTES + 4 * STATE_WORDS + 4 + text_bytes + band_bytes))
        self.faces: dict[sk.Arm, Face] = {}
        for arm in arms:
            x, y = common.panel_at(session, arm, abs(session.dx), 30)
            face = Face(font.address, w, x, y, pad, baseline, font.line_spacing, popup_h,
                        2 * font.line_spacing + pad, angle, hysteresis, round(hold * 1e6),
                        round(list_for * 1e6))
            self.faces[arm] = face
            p = program(l, face)
            if p.here > l.state:
                raise RuntimeError(f"a program of {p.here - l.base} bytes overlaps its state")
            arm.write_arena(l.state, bytes(4 * STATE_WORDS))
            arm.write_arena(l.levels, LEVELS)
            arm.write_arena(l.text, bytes(text_bytes))
            arm.load(p)
        self.ring = [["", "", ""] for _ in range(SLOTS)]
        self.head = 0
        self.count = 0

    def start(self) -> None:
        for arm in self.arms:
            arm.start(self.layout.base)

    def fits(self, text: str, room: int) -> bool:
        return (self.font.measure(text).width <= room
                and len(common.varints(self.font.glyph_indices(text))) < LINE_BYTES)

    def fit(self, text: str, room: int, last: bool) -> tuple[str, str]:
        """The longest start of the text that fits the line, with "..." when cut on the last
        line, and the rest."""
        text = text.strip()
        if self.fits(text, room):
            return text, ""
        cut = len(text)
        while cut > 0:
            cut -= 1
            if last:
                if self.fits(shown := text[:cut].strip() + "...", room):
                    return shown, ""
                continue
            if (space := text.rfind(" ", 0, cut)) > 0:
                cut = space
            if self.fits(text[:cut], room):
                return text[:cut], text[cut:]
        return "", text

    def lines(self, app: str, summary: str, body: str, at: datetime) -> list[str]:
        """"HH:MM app: summary" and the body in two lines."""
        face = self.faces[self.arms[0]]
        room = face.w - 2 * face.pad
        title = f"{app}: {summary}" if app and app != summary else summary
        out = [self.fit(f"{at:%H:%M} {title}", room, True)[0], "", ""]
        rest = " ".join(body.split())
        for i in range(1, ENTRY_LINES):
            if rest:
                out[i], rest = self.fit(rest, room, i == ENTRY_LINES - 1)
        return out

    def record(self, text: str) -> bytes:
        v = common.varints(self.font.glyph_indices(text)) if text else b""
        return (bytes([len(v)]) + v).ljust(LINE_BYTES, b"\0")

    def push(self, app: str, summary: str, body: str, lead: float, duration: float) -> None:
        """A new slot in the ring, popping up lead seconds from now for duration seconds."""
        self.head += 1
        self.count = min(SLOTS, self.count + 1)
        slot = self.head % SLOTS
        self.ring[slot] = self.lines(app, summary, body, datetime.now())
        text = b"".join(self.record(line) for line in self.ring[slot])
        for arm in self.arms:
            arm.write_arena(self.layout.text + slot * ENTRY_LINES * LINE_BYTES, text)
            arm.write_arena(self.layout.state + 4 * W_HEAD,
                            common.words(self.head, self.count, arm.device_time(lead),
                                         round(duration * 1e6)))
            arm.raise_(sk.Trigger.USER0)

    def check_popup(self, arm: sk.Arm) -> None:
        """Reads the popup's panel rows back against the lines drawn here."""
        face = self.faces[arm]
        with sk.create_bitmap(face.w, face.popup_h) as band:
            for i, text in enumerate(self.ring[self.head % SLOTS]):
                if text:
                    self.font.draw_line(band, text, face.pad, face.baseline + i * face.spacing,
                                        gray=LEVELS[i] * 17)
            common.check_panel(arm, band.image(), face.x, face.y)


def wait(arm: sk.Arm, head: int, *states: int, timeout: float = 10.0) -> sk.Event:
    """The arm's next state NOTIFY for notification head (0: any) in one of the states."""
    while True:
        e = common.wait_notify(arm, timeout)
        if (head == 0 or e.b == head) and e.a in states:
            return e


def socket_path() -> str:
    if runtime := os.environ.get("XDG_RUNTIME_DIR"):
        return os.path.join(runtime, "scouterkit-g1.sock")
    return f"/tmp/scouterkit-g1-{os.getuid()}.sock"


def via_mirror(path: str, request: dict) -> dict | None:
    """Hands the request to a running mirror; None without one."""
    try:
        conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.settimeout(1.0)
        conn.connect(path)
    except OSError:
        return None
    with conn:
        conn.settimeout(10.0)
        conn.sendall(json.dumps(request).encode() + b"\n")
        reply = json.loads(conn.makefile().readline())
    if reply.get("error"):
        raise RuntimeError(f"mirror: {reply['error']}")
    return reply


def charset(text: str) -> str:
    return "".join(dict.fromkeys(ch for ch in text if ch >= " "))


def notify_direct(args) -> None:
    """Connects, shows the message and leaves the program running."""
    with common.connect(args) as session:
        arms = common.present()
        glasses = Glasses(session, arms, charset(args.app + args.title + args.body) + "0123456789:. ")
        glasses.start()
        for arm in arms:
            wait(arm, 0, HIDDEN)
        pushed = time.monotonic()
        glasses.push(args.app, args.title, args.body, common.lead(arms), args.timeout)
        for arm in arms:
            wait(arm, glasses.head, POPUP)
        print(f"shown on {len(arms)} arm(s) {(time.monotonic() - pushed) * 1000:.0f} ms after the push; "
              f"it hides by itself in {args.timeout:g} s")
        if args.check:
            for arm in arms:
                glasses.check_popup(arm)
            print("popup read back as drawn here")
            for arm in arms:
                wait(arm, glasses.head, HIDDEN, timeout=args.timeout + 5)
                if arm.prop(sk.Prop.PANEL):
                    raise RuntimeError(f"{arm.name} arm: hidden, but the panel is on")
            print("hidden by itself, panels off")


def main(argv: list[str] | None = None) -> int:
    p = common.parser(__doc__.splitlines()[0])
    p.add_argument("--app", default="", help="app name shown before the title")
    p.add_argument("--timeout", type=float, default=5.0, help="seconds it shows")
    p.add_argument("--direct", action="store_true",
                   help="connect to the glasses even if a mirror runs (it cannot then: one link per arm)")
    p.add_argument("--socket", default=socket_path(), help="the mirror's socket")
    p.add_argument("--check", action="store_true",
                   help="read the popup back, then wait until it hides (direct only)")
    p.add_argument("title")
    p.add_argument("body", nargs="?", default="")
    args = p.parse_args(argv)

    if not (args.direct or args.emulator or args.check):
        request = {"App": args.app, "Summary": args.title, "Body": args.body,
                   "timeout_ms": round(args.timeout * 1000)}
        if (reply := via_mirror(args.socket, request)) is not None:
            print(f"shown through the mirror, {reply.get('ms', 0):.0f} ms after it got the message")
            return 0
    notify_direct(args)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, TimeoutError, sk.ScouterKitError) as e:
        sys.exit(f"notify: {e}")
