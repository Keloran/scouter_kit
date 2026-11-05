#!/usr/bin/env python3
"""The asset store's acceptance on one arm (g1v0 0.5.56+; docs/design-flash-assets.md item 25 under the
fingerprint protocol of items 27-28): the crc32 of the flash the store never writes, before anything is written;
a font put (--step: single-stepped, the fingerprints after every flash call, for the first put on an arm); a
bitmap put with the job's step times, and during it the PING round trips against idle and a TIMER program's
cadence against before; the arm's log checked for health and kick warnings; after a REBOOT the inventory and a
timed ASSET_LOAD of the font; the fingerprints again. The session is reset first (a resident program stops).
Usage: python3 -I asset_check.py ADDR FONT BITMAP [--step] [--name L|R] [--out DIR]   (or SOCKET --emu)
"""
import argparse
import json
import statistics
import struct
import sys
import zlib
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import g1host as g  # noqa: E402

STAGE = 0x1000   # the assets are staged here (up to 0x26800 for the 150 KB bitmap)
DATA = 0x30000   # the TIMER program's words: the period, TIME_US's and TIMER's property IDs
CODE = 0x31000   # its code, above the staging


def rtts(arm, n):
    """PING round trips without the arm's own time, ms."""
    return [arm.ping()[0] / 1000.0 for _ in range(n)]


def tasks(arm):
    return arm.vm_status()["tasks"], arm.tr.now()


def clock_program(arm):
    """START and TIMER: TIMER = TIME_US + 100 ms (10 tasks a second while nothing delays them)."""
    w = DATA // 4
    arm.write(DATA, struct.pack("<3I", 100000, 0x00, 0x22))
    code = g.Code().ins("SYS_READ", 0, w + 1).ins("ADD", 0, 0, w).ins("SYS_WRITE", w + 2, 0).ins("END")
    arm.write(CODE, code.b)
    arm.write(0x40, struct.pack("<16I", *([CODE, CODE] + [0] * 14)))
    arm.wait_idle(30)
    assert arm.vm_start(0x40) == 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("addr", help="BLE address, or the emulator bridge socket with --emu")
    ap.add_argument("font", help="a font blob (.skf), stored as type 1")
    ap.add_argument("bitmap", help="a bitmap asset (SKB1 header and rows), stored as type 2")
    ap.add_argument("--emu", action="store_true")
    ap.add_argument("--step", action="store_true", help="the font's put single-stepped (the arm's first put)")
    ap.add_argument("--name", default="arm")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    font, bitmap = Path(args.font).read_bytes(), Path(args.bitmap).read_bytes()
    arm = g.Arm(g.EmuTransport(args.addr) if args.emu else g.BleTransport(args.addr), name=args.name,
                log=lambda s: print(s, file=sys.stderr, flush=True))
    arm.setup()
    res = {"hello": arm.hello()}
    try:
        arm.health_ok()
        res["fingerprint_before"] = fp = arm.fingerprint()
        print(f"{args.name}: fingerprints {fp}", file=sys.stderr, flush=True)
        res["info_before"] = arm.asset_info()
        arm.reset_session(clear_arena=True)
        log0 = len(arm.read_log())
        if args.step:
            r, fid = arm.asset_put_stepped(font, 1, addr=STAGE, log=lambda s: print(s, file=sys.stderr, flush=True))
        else:
            r, fid = arm.asset_put(font, 1, addr=STAGE)
        res["font"] = {"result": r, "id": fid.hex(), "bytes": len(font), "stats": arm.asset_stats()}
        assert r == 0, r
        assert arm.fingerprint() == fp

        idle = rtts(arm, 20)
        clock_program(arm)
        arm.wait(1.0)
        t0 = tasks(arm)
        arm.wait(3.0)
        t1 = tasks(arm)
        start = len(arm.notices)
        r, bid = arm.asset_put(bitmap, 2, addr=STAGE, wait=False)
        busy, samples = [], [tasks(arm)]
        while not any(n["kind"] == 8 for n in arm.notices[start:]) and arm.asset_info()["state"] in (1, 2, 3):
            busy += rtts(arm, 3)
            samples.append(tasks(arm))
        result, _ = arm.asset_wait(1, start, 120)
        samples.append(tasks(arm))
        arm.vm_stop()
        assert arm.fingerprint() == fp, "a protected region changed during the bitmap's put"
        rate = lambda a, b: (b[0] - a[0]) / (b[1] - a[1])
        res["bitmap"] = {"result": result, "id": bid.hex(), "bytes": len(bitmap), "stats": arm.asset_stats()}
        res["ping_ms"] = {"idle_median": statistics.median(idle), "job_median": statistics.median(busy) if busy else None,
                          "job_max": max(busy) if busy else None, "job_samples": len(busy)}
        res["timer_tasks_per_s"] = {"before": rate(t0, t1), "during_job": rate(samples[0], samples[-1])}
        log = arm.read_log()[log0:]
        res["log_warnings"] = [l for l in log.splitlines() if "health" in l or "kick" in l]
        assert result == 0, result

        before = arm.losses()
        arm.reboot(0)
        arm.run_until(lambda: arm.losses() > before, 120, "the reboot")
        arm.setup(120)
        res["hello_after"] = arm.hello()
        arm.health_ok()
        res["list_after_reboot"] = arm.asset_list()
        assert {fid.hex(), bid.hex()} <= {e["id"] for e in res["list_after_reboot"]}
        t = arm.tr.now()
        arm.asset_load(fid, 0x10000, 0, len(font), data=font)
        arm.wait_idle(30)
        res["font_restore_s"] = round(arm.tr.now() - t, 3)
        c = arm.arena_crc(0x10000, len(font))
        res["font_restore_match"] = c["match"] and int(c["crc"], 16) == zlib.crc32(font)
        res["fingerprint_after"] = arm.fingerprint()
        # the restore: the ASSET_LOAD packet's round trip less an idle PING's
        res["font_restore_on_arm_s"] = round(res["font_restore_s"] - statistics.median(idle) / 1000.0, 3)
        res["pass"] = (res["fingerprint_after"] == fp and res["font_restore_match"] and
                       res["font_restore_on_arm_s"] < 0.1 and not res["log_warnings"] and
                       (not busy or statistics.median(busy) <= 2 * statistics.median(idle)))
    except Exception as ex:
        res.update(error=f"{type(ex).__name__}: {ex}", **{"pass": False})
    out = json.dumps(res, indent=1, default=lambda o: o.hex() if isinstance(o, bytes) else str(o))
    print(out)
    if args.out:
        Path(args.out).mkdir(parents=True, exist_ok=True)
        Path(args.out, f"asset_check-{args.name}.json").write_text(out)
    arm.tr.close()
    sys.exit(0 if res["pass"] else 1)


if __name__ == "__main__":
    main()
