"""Programs, events and the per-arm calls on the emulated arms."""

import struct

import pytest

import scouterkit as sk
from scouterkit import R


def echo(base):
    """USER0: NOTIFY the word at base + 1020, plus one."""
    p = sk.Program(base, 16)
    box = (base + 1020) // 4
    handler = p.label()
    p.bind(handler)
    p.add(R(1), box, p.const(1))
    p.notify(R(1), p.const(0))
    p.end()
    p.on(sk.Trigger.USER0, handler)
    p.changes(base + 1020, 4)
    return p


def test_program_round_trip(glasses):
    base = sk.alloc(1024)
    program = echo(base)
    sk.left.load(program)
    sk.left.start(program)
    sk.left.write_arena(base + 1020, struct.pack("<I", 41))
    sent = sk.now_us()
    sk.left.raise_(sk.Trigger.USER0)
    e = next(e for e in sk.left.events(timeout=2) if e.type == sk.EventType.NOTIFY)
    assert (e.a, e.b, e.trigger) == (42, 0, sk.Trigger.USER0) and e.host_us > sent
    status = sk.left.vm_status()
    assert status.running and status.table == base and status.tasks == 1
    sk.left.stop()
    assert sk.left.vm_status().state == 0


def test_kept_session_keeps_the_program(glasses):
    base = sk.alloc(1024)
    program = echo(base)
    sk.right.load(program)
    sk.right.start(program)
    sk.right.write_arena(base + 1020, struct.pack("<I", 6))
    assert sk.right.read_arena(base + 1020, 4) == struct.pack("<I", 6)
    sk.emulate_new_host(True)
    sk.right.raise_(sk.Trigger.USER0)
    e = next(e for e in sk.right.events(timeout=5) if e.type == sk.EventType.NOTIFY)
    assert e.a == 7 and sk.right.vm_status().running


def test_halt_and_host_notify(glasses):
    base = sk.alloc(256)
    p = sk.Program(base, 4)
    entry = p.label()
    p.bind(entry)
    p.halt()
    p.on(sk.Trigger.START, entry)
    sk.right.load(p)
    sk.right.start(p)
    e = sk.right.next_event(2)
    assert e.type == sk.EventType.HALT and e.code == 0 and e.address == p.code_address
    assert sk.right.vm_status().state == 2
    sk.right.notify(7, 8)
    e = sk.right.next_event(2)
    assert (e.type, e.a, e.b, e.trigger) == (sk.EventType.NOTIFY, 7, 8, 0xFF)
    assert sk.right.next_event(0.05) is None
    assert list(sk.right.events(timeout=0.05)) == []


def test_host_code(glasses):
    base = sk.alloc(64)
    sk.left.write_arena(base, struct.pack("<II", 1234, 5678))
    code = sk.Program(base, 0)
    code.notify(base // 4, base // 4 + 1)
    sk.left.run(code)
    e = sk.left.next_event(2)
    assert (e.type, e.a, e.b) == (sk.EventType.NOTIFY, 1234, 5678)


def test_arena(glasses):
    base = sk.alloc(64)
    sk.left.write_arena(base, b"hello")
    assert sk.left.read_arena(base, 5) == b"hello"
    assert sk.left.mirror(base, 5) == b"hello"
    got, want = sk.left.arena_crc(base, 64)
    assert got == want
    with pytest.raises(ValueError):
        sk.left.mirror(sk.ARENA_BYTES - 2, 4)


def test_properties(glasses):
    sk.left.set_emulated_property(sk.Prop.BATT_MV, 3812)
    assert sk.left.battery().millivolts == 3812
    sk.left.set_brightness(20)
    sk.left.notify(1, 2)
    assert sk.left.next_event(2).a == 1
    assert sk.left.prop(sk.Prop.BRIGHTNESS) == 20
    assert len(sk.left.props(sk.Prop.PANEL, sk.Prop.BATT_PCT, sk.Prop.IN_CASE)) == 3
    with pytest.raises(ValueError):
        sk.left.props()
    with pytest.raises(ValueError):
        sk.left.set_brightness(43)
    mask = 1 << sk.Trigger.TILT | 1 << sk.Trigger.USER3
    assert sk.left.report_inputs(mask) == mask & sk.left.state().triggers


def test_clocks(glasses):
    now = sk.left.device_time()
    later = sk.left.device_time(1.0)
    assert abs((later - now) % 2**32 - 1_000_000) < 5_000
    assert sk.now_us() > 0


def test_disconnect(glasses):
    reconnects = sk.right.state().reconnects
    sk.right.disconnect(0.2)
    for _ in range(100):
        s = sk.right.state()
        if s.connected and s.reconnects > reconnects:
            break
        sk.right.next_event(0.05)
    assert s.connected and s.reconnects > reconnects and s.epoch_changes == 0


def test_video(glasses):
    w, h, stride = 75, 40, 12
    bitmap, state, rec = sk.alloc(stride * h), sk.alloc(sk.VIDEO_STATE), sk.alloc(4096)
    enc = sk.VideoEncoder(w, h)
    assert enc.stride == 10
    for size in ((0, 10), (641, 10), (10, 481)):
        with pytest.raises(ValueError):
            sk.VideoEncoder(*size)
    with pytest.raises(ValueError):
        enc.encode(bytes(w * h - 1))
    for arm in (sk.left, sk.right):
        arm.write_arena(bitmap, bytes(stride * h))
    for k in range(8):
        pix = bytes(1 if (x - 20 - 4 * k) ** 2 + (y - 20) ** 2 < 150 else 0 for y in range(h) for x in range(w))
        assert enc.encode(pix, max_bytes=1) is None
        r = enc.encode(pix, redraw=k == 0)
        for arm in (sk.left, sk.right):
            arm.write_arena(rec, r)
            arm.video(bitmap, stride, w, h, 100, 50, 15, rec, len(r), state)
        m = sk.left.mirror(bitmap, stride * h)
        assert all((m[y * stride + x // 8] >> (7 - x % 8) & 1) == pix[y * w + x] for y in range(h) for x in range(w))
    for arm in (sk.left, sk.right):
        got, want = arm.panel_crc()
        assert got == want
        got, want = arm.arena_crc()
        assert got == want
