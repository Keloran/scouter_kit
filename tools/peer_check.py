#!/usr/bin/env python3
"""On-arm checks of the arm-to-arm link (VM.md §6.9; g1v0 0.5.24+), both arms connected: PEER_LINK = 1 on both;
n values from the right arm's program to the left arm's, each echoed back (the round trip on the right arm's clock,
the one-way time and PEER_T_US through the host's clock model of each arm); the eye-sync recipe (both programs draw a
band at the sender's PEER_SEND time + lead, without the host; the DRAW end times compared through the host's clocks
and through the link's own clock offset; both panels read back); the left arm following the right arm's BRIGHTNESS
(AUTO_BRIGHTNESS = 1); the link's counters (DIAG PEER). The sessions are reset first (a resident program stops);
everything else is set back at the end. JSON on stdout.
  python3 peer_check.py LEFT_ADDR RIGHT_ADDR [--n 20] [--lead-ms 200] [--out FILE]
The emulator tests (g1_firmware/emulator/tests/v5_tests.py) run the same programs."""
import argparse
import json
import statistics
import struct
import sys
import time
import zlib
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

PEER = 9
BAND_Y, BAND_H = 200, 16
ECHO = 0x10000


def band(arm, level):
    """A 640 x 16 band in the arena (full panel rows, 4 bpp): its byte address and the CRC-32 of those rows."""
    w = arm.alloc(320 * BAND_H)
    data = bytes([level << 4 | level]) * (320 * BAND_H // 2) + bytes([level << 4]) * (320 * BAND_H // 2)
    arm.write_arena(w, data)
    return 4 * w, zlib.crc32(data)


def start(arm, handlers, table=0x40, base=0xC000):
    """handlers {trigger: Code} written from base, the trigger table at `table`; returns the next free address."""
    entries, addr = [0] * 16, base
    for trig, c in handlers.items():
        arm.write(addr, c.b)
        entries[trig] = addr
        addr += len(c)
    arm.write(table, struct.pack("<16I", *entries))
    return addr


def programs(arm, side, band_addr, lead_us):
    """Right (table 0x40): USER0 sends the next value (r1 + 1) and NOTIFYs (its write time, the value); USER1 sends
    its write time and draws the band at that time + lead (the recipe), then NOTIFYs (TIME_US after the DRAW, the
    target); PEER NOTIFYs (TIME_US at arrival, PEER_MSG) and (PEER_T_US, PEER_MSG). Left: table 0x40, PEER echoes
    PEER_MSG + 0x10000 and NOTIFYs as the right's PEER handler; table 0x80, PEER draws the band at PEER_T_US + lead
    and NOTIFYs (TIME_US after the DRAW, the target). Starts table 0x40."""
    k = arm.k
    p = {n: k(g.PROPS[n]) for n in ("TIME_US", "PEER_SEND", "PEER_MSG", "PEER_T_US")}
    draw = ("DRAW", k(band_addr), k(320), k(0), k(0), k(0), k(BAND_Y), k(640), k(BAND_H))
    recv = g.Code().ins("SYS_READ", 2, p["TIME_US"]).ins("SYS_READ", 3, p["PEER_MSG"]).ins("SYS_READ", 4, p["PEER_T_US"])
    if side == "R":
        recv.ins("NOTIFY", 2, 3).ins("NOTIFY", 4, 3).ins("END")
        send = (g.Code().ins("SYS_READ", 0, p["TIME_US"]).ins("ADD", 1, 1, k(1)).ins("SYS_WRITE", p["PEER_SEND"], 1)
                .ins("NOTIFY", 0, 1).ins("END"))
        sync = (g.Code().ins("SYS_READ", 0, p["TIME_US"]).ins("SYS_WRITE", p["PEER_SEND"], 0)
                .ins("ADD", 5, 0, k(lead_us)).ins("SLEEP_UNTIL", 5).ins(*draw).ins("SYS_READ", 6, p["TIME_US"])
                .ins("NOTIFY", 6, 5).ins("END"))
        start(arm, {0: g.Code().ins("END"), PEER: recv, 12: send, 13: sync})
    else:
        echo = recv.ins("ADD", 5, 3, k(ECHO)).ins("SYS_WRITE", p["PEER_SEND"], 5).ins("NOTIFY", 2, 3).ins("NOTIFY", 4, 3)
        free = start(arm, {0: g.Code().ins("END"), PEER: echo.ins("END")})
        sync = (g.Code().ins("SYS_READ", 4, p["PEER_T_US"]).ins("ADD", 5, 4, k(lead_us)).ins("SLEEP_UNTIL", 5)
                .ins(*draw).ins("SYS_READ", 6, p["TIME_US"]).ins("NOTIFY", 6, 5).ins("END"))
        start(arm, {PEER: sync}, table=0x80, base=free)
    arm.wait_idle(30)
    if arm.vm_start(0x40) != 0:
        raise RuntimeError(f"{arm.name}: VM_START refused")


def arrivals(notifies):
    """The PEER handlers' two NOTIFYs per value: {value: TIME_US at arrival}, {value: PEER_T_US}."""
    arrival, t_us = {}, {}
    for n in notifies:
        if n["source"] == PEER:
            (t_us if n["b"] in arrival else arrival).setdefault(n["b"], n["a"])
    return arrival, t_us


def connect(addr, name):
    for attempt in range(4):
        try:
            arm = g.Arm(g.BleTransport(addr), name=name, log=lambda s: None)
            arm.setup(60)
            arm.hello()
            return arm
        except Exception as ex:
            print(f"[{name}] connect attempt {attempt}: {ex}", file=sys.stderr, flush=True)
            time.sleep(5)
    raise RuntimeError(f"{name}: no connection")


def pump_all(arms, cond, timeout=10.0, what="condition"):
    """Pump both arms in turn until cond() (an arm's packets and events move only while it is pumped)."""
    end = time.monotonic() + timeout
    while not cond():
        if time.monotonic() > end:
            raise TimeoutError(f"{what} not reached within {timeout} s")
        for a in arms:
            a.pump()


def stats(values):
    v = sorted(values)
    return {"n": len(v), "min": round(v[0], 2), "median": round(statistics.median(v), 2), "max": round(v[-1], 2)}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("left")
    ap.add_argument("right")
    ap.add_argument("--n", type=int, default=20)
    ap.add_argument("--lead-ms", type=int, default=200)
    ap.add_argument("--out")
    args = ap.parse_args()
    L, R = connect(args.left, "L"), connect(args.right, "R")
    arms = (L, R)
    for a in arms:
        a.reset_session(clear_arena=True)   # the programs count in r1 from 0
    res = {"versions": [a.info["version"] for a in arms]}
    for a in arms:
        if not a.info["triggers"] >> PEER & 1:
            raise SystemExit(f"{a.name}: firmware without the arm-to-arm link (HELLO.triggers)")
    level0 = {a.name: a.props("BRIGHTNESS")["BRIGHTNESS"] for a in arms}
    bands = {a.name: band(a, 9 if a.name == "R" else 6) for a in arms}
    for a in arms:
        programs(a, a.name, bands[a.name][0], args.lead_ms * 1000)
        a.ins("SYS_WRITE", g.PROPS["PEER_LINK"], 1)
        a.flush()
    pump_all(arms, lambda: all(a.props("PEER_LINK")["PEER_LINK"] == 2 for a in arms), 10, "PEER_LINK 2 on both")
    for a in arms:
        a.sync_clock(16)
    for i in range(args.n):
        R.add(g.Code().ins("RAISE", R.k(12)))
        R.flush()
        want = i + 1 + ECHO
        pump_all(arms, lambda: any(n["source"] == PEER and n["b"] == want for n in R.notifies), 5, f"echo {i + 1}")
        t = time.monotonic() + 0.05 + 0.013 * (i % 7)
        pump_all(arms, lambda: time.monotonic() > t, 5)
    sent = {n["b"]: n["a"] for n in R.notifies if n["source"] == 12}
    got, peer_t = arrivals(L.notifies)
    echo, _ = arrivals(R.notifies)
    echo = {v - ECHO: t for v, t in echo.items() if v > ECHO}
    common = sorted(set(sent) & set(got) & set(echo))
    res["values"] = {"sent": len(sent), "arrived": len(got), "echoed": len(echo)}
    res["round_trip_ms"] = stats([(echo[v] - sent[v]) / 1000 for v in common])
    res["right_to_left_ms_by_host_clocks"] = stats([(L.to_host(got[v]) - R.to_host(sent[v])) * 1000 for v in common])
    res["peer_t_us_minus_write_ms_by_host_clocks"] = stats([(L.to_host(peer_t[v]) - R.to_host(sent[v])) * 1000
                                                            for v in common])
    for a in arms:   # the timed DRAWs without the panel's power-on
        a.set_panel(1)
        a.flush()
    pump_all(arms, lambda: all(a.idle() for a in arms), 10)
    mark = len(L.notifies)
    if L.vm_start(0x80) != 0:
        raise RuntimeError("L: VM_START refused")
    r_draw, l_draw = [], []
    for i in range(5):
        R.add(g.Code().ins("RAISE", R.k(13)))
        R.flush()
        pump_all(arms, lambda: len([n for n in R.notifies if n["source"] == 13]) > i and
                 len([n for n in L.notifies[mark:] if n["source"] == PEER]) > i, 5, f"sync draw {i + 1}")
        t = time.monotonic() + 0.3
        pump_all(arms, lambda: time.monotonic() > t, 5)
    r_draw = [n for n in R.notifies if n["source"] == 13]
    l_draw = [n for n in L.notifies[mark:] if n["source"] == PEER]
    peer = {a.name: a.peer_stats() for a in arms}
    off = peer["L"]["offset_us"]
    res["sync_by_host_clocks_ms"] = [round((L.to_host(l["a"]) - R.to_host(r["a"])) * 1000, 2) for r, l in zip(r_draw, l_draw)]
    res["sync_by_link_offset_us"] = [(l["a"] - off) - r["a"] for r, l in zip(r_draw, l_draw)] if off is not None else None
    res["late_draws"] = sum(1 for n in r_draw + l_draw if n["a"] - n["b"] > 100000)
    res["draw_end_after_target_ms"] = {"R": [round((n["a"] - n["b"]) / 1000, 2) for n in r_draw],
                                       "L": [round((n["a"] - n["b"]) / 1000, 2) for n in l_draw]}
    res["panels"] = {}
    for a in arms:
        rb = a.panel_read(BAND_Y, BAND_H)
        res["panels"][a.name] = {"crc_ok": rb.get("crc") == f"{bands[a.name][1]:08x}", "status": rb["status"]}
    L.ins("SYS_WRITE", g.PROPS["AUTO_BRIGHTNESS"], 1)
    L.flush()
    follow = []
    for level in (30, 12):
        R.ins("SYS_WRITE", g.PROPS["BRIGHTNESS"], level)
        R.flush()
        pump_all(arms, lambda: R.idle(), 5)
        t0 = time.monotonic()
        pump_all(arms, lambda: L.props("BRIGHTNESS")["BRIGHTNESS"] == level, 5, f"left follows {level}")
        follow.append(round((time.monotonic() - t0) * 1000))
    res["follow_ms_upper_bound"] = follow
    for a in arms:
        a.vm_stop()
        a.ins("SYS_WRITE", g.PROPS["PEER_LINK"], 0)
        a.ins("SYS_WRITE", g.PROPS["BRIGHTNESS"], level0[a.name])
        a.set_panel(0)
        a.flush()
    pump_all(arms, lambda: all(a.idle() for a in arms), 10)
    res["after"] = {a.name: a.props("PEER_LINK", "AUTO_BRIGHTNESS", "BRIGHTNESS", "PANEL") for a in arms}
    res["peer_stats"] = peer
    res["clock_err_ms"] = {a.name: round(a.offset_err_us / 1000, 2) for a in arms}
    text = json.dumps(res, indent=1)
    print(text)
    if args.out:
        Path(args.out).write_text(text + "\n")
    for a in arms:
        a.tr.close()


if __name__ == "__main__":
    main()
