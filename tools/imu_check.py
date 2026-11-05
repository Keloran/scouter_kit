#!/usr/bin/env python3
"""On-arm checks of the accelerometer (M3, g1v0 0.5.4+) that need no movement: readings and their spread at
rest, MOTION from the sensor noise at a tiny MOTION_MG (and none at a large one), TILT when the range is
moved past the current pitch, both as INPUT events (EVENTS mask) and as a program's triggers (NOTIFY), and
the CPU wake-ups (INT1) at rest while TILT is armed.

  python3 -I imu_check.py --ble ADDR --name L --out logs/imu/1"""
import argparse
import json
import statistics
import struct
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

M = g.M32


def write_props(arm, pairs):
    c = g.Code()
    for pid, v in pairs:
        c.ins("SYS_WRITE", arm.k(pid), arm.k(v & M))
    arm.add(c)
    arm.wait_idle(10)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--ble", required=True)
    ap.add_argument("--name", default="arm")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    arm = g.Arm(g.BleTransport(args.ble), name=args.name)
    arm.setup()
    arm.hello()
    arm.reset_session()
    out = {"hello": {k: arm.info[k] for k in ("version", "role", "triggers")}}
    rows = []
    for _ in range(20):
        rows.append(arm.imu())
        arm.wait(0.1)
    out["rest"] = {k: {"mean": round(statistics.mean(r[k] for r in rows), 1), "sd": round(statistics.pstdev(r[k] for r in rows), 2)}
                   for k in ("ACC_X", "ACC_Y", "ACC_Z", "PITCH", "ROLL")}
    out["g_mg"] = round(sum(out["rest"][k]["mean"] ** 2 for k in ("ACC_X", "ACC_Y", "ACC_Z")) ** 0.5)
    # MOTION from noise: MOTION_MG 1 fires on nearly every sample pair, 2000 never
    assert arm.events_mask(1 << 2 | 1 << 3) == 0x000C
    n0 = len(arm.inputs)
    write_props(arm, [(0x23, 1)])
    i0 = arm.imu_stats()["irqs"] or 0
    arm.wait(5.0)
    i1 = arm.imu_stats()["irqs"] or 0
    tiny = [x for x in arm.inputs[n0:] if x["trigger"] == 2]
    write_props(arm, [(0x23, 2000)])
    arm.wait(1.0)
    n1 = len(arm.inputs)
    arm.wait(5.0)
    large = [x for x in arm.inputs[n1:] if x["trigger"] == 2]
    out["motion"] = {"inputs_5s_at_1mg": len(tiny), "int1_irqs_5s_at_1mg": i1 - i0,
                     "values_mg": sorted(x["value"] for x in tiny)[-5:], "inputs_5s_at_2000mg": len(large)}
    write_props(arm, [(0x23, 0)])
    # TILT: the range moved past the current pitch fires once; inside again, then out again, once more
    p = arm.props("PITCH")["PITCH"]
    n0 = len(arm.inputs)
    write_props(arm, [(0x24, p + 5), (0x25, 90)])
    arm.wait(1.0)
    first = [x for x in arm.inputs[n0:] if x["trigger"] == 3]
    write_props(arm, [(0x24, -90), (0x25, 90)])
    arm.wait(1.0)
    write_props(arm, [(0x24, -90), (0x25, p - 5)])
    arm.wait(1.0)
    both = [x for x in arm.inputs[n0:] if x["trigger"] == 3]
    out["tilt_input"] = {"pitch": p, "after_first_move": [(x["value"]) for x in first],
                         "after_second_move": [x["value"] for x in both]}
    # a program: TILT -> NOTIFY 3, PITCH; MOTION -> NOTIFY 2, 0; the range set so that it is left at once
    k = arm.k
    tilt = g.Code().ins("SYS_READ", 1, k(0x1B)).ins("NOTIFY", k(3), 1).ins("END")
    motion = g.Code().ins("NOTIFY", k(2), k(0)).ins("END")
    start = g.Code().ins("END")
    for addr, c in ((0xC000, tilt), (0xC100, motion), (0xC200, start)):
        arm.write(addr, c.b)
    table = [0] * 16
    table[0], table[2], table[3] = 0xC200, 0xC100, 0xC000
    arm.write(0x40, struct.pack("<16I", *table))
    arm.wait_idle(10)
    write_props(arm, [(0x24, -90), (0x25, 90), (0x23, 0)])
    assert arm.events_mask(0) == 0
    assert arm.vm_start(0x40) == 0
    arm.wait(0.5)
    s0 = arm.imu_stats()
    arm.wait(4.0)
    s1 = arm.imu_stats()
    out["streaming_idle"] = {"streaming": s1["streaming"], "irqs_4s": (s1["irqs"] or 0) - (s0["irqs"] or 0)}
    write_props(arm, [(0x24, p + 5)])
    arm.wait(4.0)
    s2 = arm.imu_stats()
    out["armed_at_rest"] = {"streaming": s2["streaming"], "irqs_4s": (s2["irqs"] or 0) - (s1["irqs"] or 0),
                            "samples_4s": s2["samples"] - s1["samples"]}
    n0 = len(arm.notifies)
    write_props(arm, [(0x23, 1)])
    arm.wait(3.0)
    out["program"] = {"tilt_notifies": [(x["a"], x["b"]) for x in arm.notifies if x["a"] == 3][:3],
                      "motion_notifies_3s": sum(1 for x in arm.notifies[n0:] if x["a"] == 2)}
    assert arm.vm_stop() == 0
    write_props(arm, [(0x23, 0), (0x24, -90), (0x25, 90)])
    arm.wait(0.5)
    out["stats_end"] = arm.imu_stats()
    out["halts"] = arm.halts
    Path(args.out).mkdir(parents=True, exist_ok=True)
    Path(args.out, "imu_check.json").write_text(json.dumps(out, indent=1, default=str))
    print(json.dumps(out, indent=1, default=str))
    arm.tr.close()


if __name__ == "__main__":
    main()
