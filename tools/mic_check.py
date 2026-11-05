#!/usr/bin/env python3
"""On-arm check of the microphone (M5, g1v0 0.5.8+), within these privacy rules: short bursts
(a few seconds each), the indicator on, and only aggregate numbers kept. Every frame is decoded in memory as it
arrives and dropped after its counter, decode status and level are counted; no audio is stored or sent on.

Per burst: MIC_DATA events and frames, rate, counter gaps, frames the decoder rejects, the decoded level (RMS and
peak, dBFS) and the arm's own DIAG MIC counters (frames, lost, encode time). The indicator is checked by PANEL_READ
(rows 4-19: the 16 x 16 block at level 15 at (620, 4), nothing else). The current: the arm's per-state accumulators
(connected, panel on, on battery) over the mic-off and mic-on halves of each round. Then a disconnection with the
microphone on: after the reconnect DIAG MIC must read off and the indicator be gone.

  python3 -I mic_check.py --ble RIGHT_ADDR --rounds 4 --seconds 5 --out logs/mic/1"""
import argparse
import json
import math
import os
import struct
import sys
import zlib
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

STATE_CONNECTED_PANEL_ON = 3


def rows_crc(arm, indicator):
    """CRC-32 of panel rows 4-19 as the mirror has them with the indicator block (x 620-635) at level 15, or
    cleared to level 0 as the firmware leaves it (VM.md §6.5)"""
    rows = bytearray(arm.mirror.panel_rows(4, 16)) if arm.mirror.panel_on() else bytearray(320 * 16)
    for y in range(16):
        rows[320 * y + 310:320 * y + 318] = (b"\xff" if indicator else b"\x00") * 8
    return f"{zlib.crc32(bytes(rows)):08x}"


def indicator_state(arm):
    """PANEL_READ of rows 4-19; a panel that is off (status 11) shows no indicator either"""
    r = arm.panel_read(4, 16)
    return {"status": r["status"], "crc": r.get("crc"), "block": r.get("crc") == rows_crc(arm, True),
            "clear": r.get("crc") == rows_crc(arm, False) or r["status"] == 11}


class Traffic:
    """display traffic on the same arm while it captures: 2 KB of arena writes and a full-panel DRAW per step"""

    def __init__(self, arm):
        self.arm = arm
        self.buf = arm.alloc(320 * 480)
        arm.fill(self.buf, 320, 0, 0, 640, 480, 2)
        arm.wait_idle(30)
        self.draws = self.bytes = 0
        self.ms = []

    def run(self, seconds, rate=0):
        end = self.arm.tr.now() + seconds
        renewed = self.arm.tr.now()
        while self.arm.tr.now() < end:
            if self.arm.tr.now() - renewed > 0.8:
                self.arm.mic(True, rate)
                renewed = self.arm.tr.now()
            off = 2048 * (self.draws % 70)
            self.arm.write_arena(self.buf + off // 4, os.urandom(2048))
            t = self.arm.wait_draw(self.arm.draw(self.buf, 320, 0, 0, 640, 480))
            self.arm.draws[-1]["panel"] = b""
            self.draws += 1
            self.bytes += 2048
            if t.get("panel_write_ms"):
                self.ms.append(t["panel_write_ms"])

    def result(self, seconds):
        return {"draws": self.draws, "draws_per_s": round(self.draws / seconds, 1),
                "write_kBps": round(self.bytes / seconds / 1000, 1),
                "draw_ms": round(sum(self.ms) / len(self.ms), 1) if self.ms else None}


def dbfs(v):
    return round(20 * math.log10(v / 32768), 1) if v > 0 else None


class Burst:
    """aggregates of one burst; each frame is decoded and forgotten"""

    def __init__(self, frame_bytes):
        self.fb = frame_bytes
        self.dec = g.Lc3Decoder()
        self.events = self.frames = self.bad = self.gaps = 0
        self.sum_sq = self.sum = 0.0
        self.samples = 0
        self.peak = self.start_peak = 0
        self.start_frames = []  # peak of each of the first 30 frames: the start transient, no room sound to speak of
        self.win = []           # RMS of each 100 ms window after the first 500 ms: only its min and max are kept
        self.win_sq = 0.0
        self.expect = None
        self.t0 = self.t1 = None
        self.n0 = 0

    def __call__(self, first, n, frames, t_dev, t):
        self.events += 1
        if self.t0 is None:
            self.t0, self.n0 = t_dev, n
        self.t1 = t_dev
        if self.expect is not None and first != self.expect:
            self.gaps += (first - self.expect) & 0xFFFF
        self.expect = (first + n) & 0xFFFF
        for k in range(n):
            pcm, st = self.dec.decode(bytes(frames[k * self.fb:(k + 1) * self.fb]))
            self.frames += 1
            self.bad += st != 0
            if self.frames <= 50:
                pk = max(abs(x) for x in pcm)
                self.start_peak = max(self.start_peak, pk)
                if self.frames <= 30:
                    self.start_frames.append(dbfs(pk))
                continue
            sq = sum(x * x for x in pcm)
            self.sum_sq += sq
            self.sum += sum(pcm)
            self.samples += len(pcm)
            self.peak = max(self.peak, max(abs(x) for x in pcm))
            self.win_sq += sq
            if self.frames % 10 == 0:
                self.win.append(math.sqrt(self.win_sq / 1600))
                self.win_sq = 0.0
                self.win = [min(self.win), max(self.win)]

    def result(self):
        """frames per second by the arm's clock (the frames after the first event over the events' span); levels
        of the decoded audio after its first 500 ms: RMS, DC, AC RMS, peak, the quietest and loudest 100 ms
        window; and the peak of the first 500 ms (the PDM filter's start)"""
        n = self.samples or 1
        rms = math.sqrt(self.sum_sq / n)
        dc = self.sum / n
        ac = math.sqrt(max(self.sum_sq / n - dc * dc, 0.0))
        span = g.signed32(self.t1 - self.t0) / 1e6 if self.events > 1 else 0
        return {"events": self.events, "frames": self.frames, "span_s": round(span, 2),
                "frames_per_s": round((self.frames - self.n0) / span, 1) if span > 0 else None,
                "gaps": self.gaps, "bad_frames": self.bad, "rms_dbfs": dbfs(rms), "dc": round(dc, 1),
                "ac_rms_dbfs": dbfs(ac), "peak_dbfs": dbfs(self.peak),
                "window_rms_dbfs": [dbfs(v) for v in self.win], "start_peak_dbfs": dbfs(self.start_peak),
                "start_frame_peaks_dbfs": self.start_frames}


def state_sum(arm):
    """the accumulators of "connected, panel on": on battery (state 3) and on VBUS (state 7)"""
    return arm.power(1 + STATE_CONNECTED_PANEL_ON), arm.power(5 + STATE_CONNECTED_PANEL_ON)


def state_delta(a, b):
    """mA on battery only (VBUS samples are the charger's); CPU % over both"""
    n = b[0]["ua_samples"] - a[0]["ua_samples"]
    cyc = sum(y["all_cycles"] - x["all_cycles"] for x, y in zip(a, b))
    busy = sum(y["busy_cycles"] - x["busy_cycles"] for x, y in zip(a, b))
    return {"samples": n, "ma": round((b[0]["sum_ua"] - a[0]["sum_ua"]) / n / 1000, 2) if n else None,
            "cpu_pct": round(100 * busy / cyc, 2) if cyc else None}


def check(arm, rounds, seconds, rate, drop_link, need_encrypted=True, draw=False, clock=None):
    """the bursts, the current per half-round and the disconnection; returns the aggregate record. draw: display
    traffic on the arm during each burst; clock: DIAG SET 5 first (test builds: 1 = capture at 128 MHz)"""
    seconds = min(seconds, 6.0)
    h = arm.hello()
    out = {"hello": {k: h[k] for k in ("version", "role", "mic", "encrypted")}, "rounds": [], "draw": draw,
           "clock_128": clock}
    assert h["mic"] and (h["encrypted"] or not need_encrypted), out
    if clock is not None:
        arm.ctrl_ok("DIAG", bytes([5, 5, clock]))
    arm.set_panel(1)
    arm.wait_idle(10)
    arm.wait(1.0)
    traffic = Traffic(arm) if draw else None
    out["indicator_before"] = indicator_state(arm)
    for rnd in range(rounds):
        r = {}
        s0 = state_sum(arm)
        arm.wait(seconds)
        s1 = state_sum(arm)
        r["off"] = state_delta(s0, s1)
        burst = Burst(40 if rate == 0 else 20)
        arm.on_mic = burst
        rep = arm.mic(True, rate)
        assert rep["status"] == 0, rep
        r["format"] = {k: rep[k] for k in ("frame_bytes", "frames_per_event", "sample_rate", "frame_ms")}
        s2 = state_sum(arm)
        if traffic:
            traffic.run(seconds / 2, rate)
        else:
            arm.mic_hold(seconds / 2, rate)
        arm.mic(True, rate)
        r["indicator_on"] = indicator_state(arm)
        if traffic:
            traffic.run(seconds / 2, rate)
            r["traffic"] = traffic.result(seconds)
            traffic.draws = traffic.bytes = 0
            traffic.ms = []
        else:
            arm.mic_hold(seconds / 2, rate)
        s3 = state_sum(arm)
        r["arm"] = arm.mic_stats()
        assert arm.mic(False)["status"] == 0
        n_off = burst.events
        arm.wait(0.5)
        arm.on_mic = None
        r["events_after_off"] = burst.events - n_off
        r["on"] = state_delta(s2, s3)
        r["burst"] = burst.result()
        r["indicator_off"] = indicator_state(arm)
        out["rounds"].append(r)
        print(json.dumps({"round": rnd, **r["burst"], "off_ma": r["off"]["ma"], "on_ma": r["on"]["ma"],
                          "encode_avg_us": r["arm"]["encode_avg_us"], "lost": r["arm"]["lost"]}), flush=True)
    # a disconnection while on: off after the reconnect, indicator gone
    burst = Burst(40)
    arm.on_mic = burst
    assert arm.mic(True)["status"] == 0
    arm.wait(1.0)
    before = arm.losses()
    drop_link()
    arm.run_until(lambda: arm.losses() > before, 30, "link loss")
    arm.on_mic = None
    arm.setup(60)
    arm.hello()
    n0 = arm.mic_events
    arm.wait(1.0)
    out["after_disconnect"] = {"mic_data": arm.mic_events - n0, "arm_on": arm.mic_stats()["on"],
                               "indicator": indicator_state(arm)}
    arm.set_panel(0)
    arm.wait_idle(10)
    ok = [x for x in out["rounds"] if x["burst"]["gaps"] == 0 and x["burst"]["bad_frames"] == 0
          and x["indicator_on"]["block"] and x["indicator_off"]["clear"] and x["events_after_off"] == 0]
    on_ma = [x["on"]["ma"] for x in out["rounds"] if x["on"]["ma"] is not None]
    off_ma = [x["off"]["ma"] for x in out["rounds"] if x["off"]["ma"] is not None]
    out["summary"] = {"rounds_ok": f"{len(ok)}/{len(out['rounds'])}",
                      "frames_per_s": [x["burst"]["frames_per_s"] for x in out["rounds"]],
                      "rms_dbfs": [x["burst"]["rms_dbfs"] for x in out["rounds"]],
                      "encode_avg_us": [x["arm"]["encode_avg_us"] for x in out["rounds"]],
                      "encode_max_us": max(x["arm"]["encode_max_us"] for x in out["rounds"]),
                      "cpu_pct_on": [x["on"]["cpu_pct"] for x in out["rounds"]],
                      "cpu_pct_off": [x["off"]["cpu_pct"] for x in out["rounds"]],
                      "ma_on_mean": round(sum(on_ma) / len(on_ma), 2) if on_ma else None,
                      "ma_off_mean": round(sum(off_ma) / len(off_ma), 2) if off_ma else None,
                      "after_disconnect_ok": out["after_disconnect"]["mic_data"] == 0
                      and not out["after_disconnect"]["arm_on"] and out["after_disconnect"]["indicator"]["clear"],
                      "lost": sum(x["arm"]["lost"] for x in out["rounds"]),
                      "event_drops": sum(x["arm"]["event_drops"] for x in out["rounds"]),
                      "gaps": sum(x["burst"]["gaps"] for x in out["rounds"]),
                      "traffic": [x.get("traffic") for x in out["rounds"]] if draw else None}
    if clock is not None:
        arm.ctrl_ok("DIAG", bytes([5, 5, 0]))
    print(json.dumps(out["summary"]), flush=True)
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--ble", required=True)
    ap.add_argument("--name", default="R")
    ap.add_argument("--rounds", type=int, default=4)
    ap.add_argument("--seconds", type=float, default=5.0, help="length of each burst (at most 6)")
    ap.add_argument("--rate", type=int, default=0, help="0 = 32 kbps, 1 = 16 kbps")
    ap.add_argument("--draw", action="store_true", help="display traffic on the arm during each burst")
    ap.add_argument("--clock", type=int, choices=(0, 1), help="test builds: capture at 64 (0) or 128 (1) MHz")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    arm = g.Arm(g.BleTransport(args.ble), name=args.name)
    arm.setup()
    out = check(arm, args.rounds, args.seconds, args.rate,
                lambda: arm.tr.run(arm.tr.client.disconnect(), timeout=10), draw=args.draw, clock=args.clock)
    Path(args.out).mkdir(parents=True, exist_ok=True)
    Path(args.out, "mic_check.json").write_text(json.dumps(out, indent=1))
    arm.tr.close()


if __name__ == "__main__":
    main()
