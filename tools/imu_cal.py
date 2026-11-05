#!/usr/bin/env python3
"""IMU axis calibration: the head frame of each arm's LSM6DSO from two poses.

The firmware maps the sensor axes to the head frame (x right, y up, z toward the wearer; VM.md §2.5, right-handed)
with a Q14 matrix per arm
(imu.c head_map). Run this with the glasses on: pose 1 level (looking straight ahead), pose 2 looking down
by roughly 30-60 degrees. Each arm reports ACC_X/Y/Z through the matrix it runs (0.5.8+: read from the arm by
DIAG IMU, the provisional map from stock's axes and factory pitch base; --map overrides it; the identity before
0.5.8), so the raw sensor vector is recovered first. The check also shows the pitch and roll the running map
gives in both poses, i.e. how far the provisional map is off.

  python3 -I imu_cal.py --left LEFT_ADDR --right RIGHT_ADDR --out logs/imu-cal/1

Prints the head_map rows for imu.c and, for a check, the pitch and roll each arm would report in both poses."""
import argparse
import json
import math
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

IDENTITY = [[1, 0, 0], [0, 1, 0], [0, 0, 1]]


def mean_acc(arm, seconds=2.0, every=0.1):
    rows = []
    t0 = time.time()
    while time.time() - t0 < seconds:
        p = arm.props("ACC_X", "ACC_Y", "ACC_Z")
        if None not in p.values():
            rows.append((p["ACC_X"], p["ACC_Y"], p["ACC_Z"]))
        arm.wait(every)
    n = len(rows)
    return [sum(r[i] for r in rows) / n for i in range(3)], n


def solve(m, v):
    """the raw sensor vector s with m s = v (m: the arm's current map, rows in Q14 units / 2^14)"""
    a = [row[:] + [v[i]] for i, row in enumerate(m)]
    for c in range(3):
        p = max(range(c, 3), key=lambda r: abs(a[r][c]))
        a[c], a[p] = a[p], a[c]
        for r in range(3):
            if r != c:
                f = a[r][c] / a[c][c]
                a[r] = [x - f * y for x, y in zip(a[r], a[c])]
    return [a[i][3] / a[i][i] for i in range(3)]


def norm(v):
    n = math.sqrt(sum(x * x for x in v))
    return [x / n for x in v]


def cross(a, b):
    return [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]]


def dot(a, b):
    return sum(x * y for x, y in zip(a, b))


def head_axes(level, down):
    """rows x, y, z of the head frame in sensor coordinates: y = up when level; z = toward the wearer, from the
    pose looking down (its up vector leans toward z); x = y cross z (right): a rotation, determinant +1"""
    y = norm(level)
    d = norm(down)
    z = norm([a - dot(d, y) * b for a, b in zip(d, y)])
    x = cross(y, z)
    return [x, y, z]


def angles(rows, s):
    h = [dot(r, s) for r in rows]
    clamp = lambda a: max(-90, min(90, round(a)))
    return clamp(math.degrees(math.atan2(-h[2], h[1]))), clamp(math.degrees(math.atan2(-h[0], h[1])))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--left")
    ap.add_argument("--right")
    ap.add_argument("--map", help="JSON {\"L\": 3x3, \"R\": 3x3} of the image on the arms, in units of 1 (default identity)")
    ap.add_argument("--out", help="directory for the readings and the result")
    args = ap.parse_args()
    maps = json.loads(args.map) if args.map else {}
    arms = {}
    for name, addr in (("L", args.left), ("R", args.right)):
        if addr:
            arm = g.Arm(g.BleTransport(addr), name=name)
            arm.setup()
            arm.hello()
            arms[name] = arm
            if name not in maps:
                maps[name] = arm.imu_stats().get("head_map") or IDENTITY
    raw = {n: {} for n in arms}
    for pose, prompt in (("level", "Pose 1: glasses on, head upright, looking straight ahead. Enter when still."),
                         ("down", "Pose 2: look down (nose down, about 30-60 degrees). Enter when still.")):
        input(prompt)
        for n, arm in arms.items():
            v, k = mean_acc(arm)
            raw[n][pose] = {"reported": v, "samples": k, "sensor": solve(maps[n], v)}
    out = {"raw": raw, "running_map": maps, "head_map": {}, "check": {}, "running_check": {}}
    for n in arms:
        out["running_check"][n] = {pose: angles(maps[n], raw[n][pose]["sensor"]) for pose in ("level", "down")}
        rows = head_axes(raw[n]["level"]["sensor"], raw[n]["down"]["sensor"])
        out["head_map"][n] = [[round(c * 16384) for c in r] for r in rows]
        out["check"][n] = {pose: angles(rows, raw[n][pose]["sensor"]) for pose in ("level", "down")}
        g_level = math.sqrt(sum(c * c for c in raw[n]["level"]["sensor"]))
        out["check"][n]["g_level_mg"] = round(g_level)
    text = json.dumps(out, indent=1)
    print(text)
    for n in ("R", "L"):
        if n in out["head_map"]:
            m = out["head_map"][n]
            print(f"\t{{{{{m[0][0]}, {m[0][1]}, {m[0][2]}}}, {{{m[1][0]}, {m[1][1]}, {m[1][2]}}}, "
                  f"{{{m[2][0]}, {m[2][1]}, {m[2][2]}}}}},   /* {'right' if n == 'R' else 'left'} */")
    if args.out:
        Path(args.out).mkdir(parents=True, exist_ok=True)
        Path(args.out, "imu_cal.json").write_text(text)
    for arm in arms.values():
        arm.tr.close()


if __name__ == "__main__":
    main()
