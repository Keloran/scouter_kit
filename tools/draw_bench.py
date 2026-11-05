#!/usr/bin/env python3
"""DRAW cost on an arm: the arm's own NOTIFY times around each DRAW (panel_write_ms) for a set of rectangle
sizes, each drawn `--n` times from a 4 bpp arena bitmap, then a least-squares fit of time = a·rows + b·bytes
(bytes = rows × ceil(w / 2)) per width class. Panel on, nothing else running; the panel ends off. With --video:
instead, the time of VIDEO (VM.md §3.7) for five worst-case 640 × 480 records (needs g++).

  python3 -I draw_bench.py --ble ADDR --name R --out logs/draw/1 [--video]"""
import argparse
import json
import statistics
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

SIZES = [(640, 480), (640, 120), (640, 8), (348, 197), (348, 50), (150, 150), (151, 150), (40, 40), (8, 200),
         (2, 200), (640, 1)]


def fit(rows):
    """least squares t = a·r + b·n over (r, n, t)"""
    srr = sum(r * r for r, n, t in rows)
    snn = sum(n * n for r, n, t in rows)
    srn = sum(r * n for r, n, t in rows)
    srt = sum(r * t for r, n, t in rows)
    snt = sum(n * t for r, n, t in rows)
    det = srr * snn - srn * srn
    if not det:
        return None, None
    return (srt * snn - snt * srn) / det, (snt * srr - srt * srn) / det


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--ble", required=True)
    ap.add_argument("--name", default="arm")
    ap.add_argument("--n", type=int, default=3)
    ap.add_argument("--out", required=True)
    ap.add_argument("--video", action="store_true", help="0.5.42+: VIDEO's worst cases instead of the DRAW sizes")
    args = ap.parse_args()
    arm = g.Arm(g.BleTransport(args.ble), name=args.name)
    arm.setup()
    if args.video:
        with tempfile.TemporaryDirectory() as tmp:
            recs = video_records(tmp)
        h = arm.hello()
        last = video_send(arm, recs)
        arm.run_until(lambda: last in arm.fences, 120, "the last VIDEO")
        out = {"hello": {k: h[k] for k in ("version", "role")}, "record_bytes": [len(r) for r in recs],
               "video_ms": video_ms(arm, len(recs)), "panel": arm.panel_read(0, 480)}
        print(json.dumps(out), flush=True)
        arm.set_panel(0)
        arm.wait_idle(10)
    else:
        out = bench(arm, args.n)
    Path(args.out).mkdir(parents=True, exist_ok=True)
    Path(args.out, "draw_bench.json").write_text(json.dumps(out, indent=1))
    arm.tr.close()


def bench(arm, n=3, sizes=SIZES):
    h = arm.hello()
    buf = arm.alloc(320 * 480)
    arm.fill(buf, 320, 0, 0, 640, 480, 5)
    arm.fill(buf, 320, 100, 100, 200, 200, 12)
    arm.wait_idle(30)
    out = {"hello": {k: h[k] for k in ("version", "role")}, "sizes": []}
    for w, hh in sizes:
        ms = []
        for _ in range(n):
            t = arm.wait_draw(arm.draw(buf, 320, 0, 0, w, hh))
            arm.draws[-1]["panel"] = b""
            if t.get("panel_write_ms") is not None:
                ms.append(t["panel_write_ms"])
        med = statistics.median(ms) if ms else None
        row = {"w": w, "h": hh, "bytes": hh * ((w + 1) // 2), "ms": [round(x, 3) for x in ms],
               "median_ms": round(med, 3) if med is not None else None,
               "us_per_row": round(1000 * med / hh, 1) if med is not None else None}
        out["sizes"].append(row)
        print(json.dumps(row), flush=True)
    pts = [(s["h"], s["bytes"], 1000 * s["median_ms"]) for s in out["sizes"] if s["median_ms"] is not None]
    def fitted(points):
        a, b = fit(points) if len(points) > 1 else (None, None)
        return None if a is None else {"us_per_row": round(a, 1), "us_per_byte": round(b, 3)}
    out["fit_all"] = fitted(pts)
    out["fit_full_width"] = fitted([p for p, s in zip(pts, out["sizes"]) if s["w"] == 640])
    out["fit_partial"] = fitted([p for p, s in zip(pts, out["sizes"]) if s["w"] != 640])
    print(json.dumps({k: out[k] for k in ("fit_all", "fit_full_width", "fit_partial")}), flush=True)
    out["power_on"] = power_on(arm, buf, n)
    print(json.dumps(out["power_on"]), flush=True)
    arm.set_panel(0)
    arm.wait_idle(10)
    return out


VIDEO_MARK = 0x7100


def video_records(out_dir):
    """the worst-case VIDEO records of ../tests/video_records.cpp, built here with g++ from the library's encoder:
    640 × 480 one-pixel stripes with the redraw flag, and 12-pixel stripes whose every transition is V(±5)"""
    sk = Path(__file__).resolve().parent.parent
    exe = Path(out_dir) / "video_records"
    subprocess.run(["g++", "-std=c++20", "-O2", "-DSCOUTER_G1_EMULATOR", str(sk / "tests/video_records.cpp"),
                    str(sk / "src/base/scouter_kit.cpp"), str(sk / "src/vm/vm.c"), str(sk / "src/vm/vm_gfx.c"),
                    "-o", str(exe), "-lpthread"], check=True)
    r = subprocess.run([str(exe)], check=True, capture_output=True, text=True)
    return [bytes.fromhex(x) for x in r.stdout.split()]


def video_send(arm, recs):
    """the records as VIDEOs (640 × 480 at (0, 0), back to back), each between NOTIFYs VIDEO_MARK + 2i and + 2i + 1;
    returns the last mark"""
    bm, st = arm.alloc(80 * 480), arm.alloc(1600)
    bufs = [arm.alloc(len(r) + 3) for r in recs]
    arm.write_arena_rle(bm, bytes(80 * 480))
    for b, r in zip(bufs, recs):
        arm.write_arena(b, r)
    arm.wait_idle(60)
    k = arm.k
    for i, (b, r) in enumerate(zip(bufs, recs)):
        arm.add(g.Code().ins("NOTIFY", k(VIDEO_MARK + 2 * i), k(0)).ins(
            "VIDEO", k(4 * bm), k(80), k(640), k(480), k(0), k(0), k(15), k(4 * b), k(len(r)), k(4 * st)).ins(
            "NOTIFY", k(VIDEO_MARK + 2 * i + 1), k(0)))
    arm.flush()
    return VIDEO_MARK + 2 * len(recs) - 1


def video_ms(arm, n):
    """the arm's time of each VIDEO sent by video_send, ms"""
    f = arm.fences
    return [round((f[VIDEO_MARK + 2 * i + 1]["t_dev"] - f[VIDEO_MARK + 2 * i]["t_dev"]) / 1000, 1) for i in range(n)]


def power_on(arm, buf, n):
    """a 40 x 40 DRAW with the panel on, after PANEL = 0 (the DRAW powers it on) and, on test builds, after DIAG
    SET 6 standby 1 / 2 (deep power-down with the rails on / VCOM off too); each with the firmware's own
    power-on time (DIAG SET 6 0xFF)"""
    def one():
        t = arm.wait_draw(arm.draw(buf, 320, 300, 200, 40, 40))
        arm.draws[-1]["panel"] = b""
        rep = arm.ctrl("DIAG", bytes([5, 6, 0xFF]))
        fw = struct.unpack_from("<I", rep["body"])[0] if rep["status"] == 0 and len(rep["body"]) >= 4 else None
        return t.get("panel_write_ms"), fw
    res = {"on": [], "from_off": [], "from_standby1": [], "from_standby2": []}
    one()
    for _ in range(n):
        res["on"].append(one())
    for _ in range(n):
        arm.set_panel(0)
        arm.wait_idle(10)
        arm.wait(0.3)
        res["from_off"].append(one())
    for mode in (1, 2):
        for _ in range(n):
            if arm.ctrl("DIAG", bytes([5, 6, mode]))["status"] != 0:
                break
            arm.mirror.set_panel(0)
            arm.wait(0.3)
            res[f"from_standby{mode}"].append(one())
    return {k: {"draw_ms": [round(a, 2) for a, b in v if a is not None], "power_on_ms": [b for a, b in v]}
            for k, v in res.items()}


if __name__ == "__main__":
    main()
