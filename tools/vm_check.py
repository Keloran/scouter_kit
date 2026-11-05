#!/usr/bin/env python3
"""Protocol 5 checks on an arm or the DK (g1v0 0.5.x) with g1host.py: the VM.md §9.2 clock and §9.3
battery programs from the bytes printed in VM.md (start, a minute tick at N', the RAISE C12 correction;
the battery band at the arm's BATT_PCT), each frame compared with the mirror through PANEL_READ; VM_STATUS,
a TIMER handler's lateness, the microphone indicator (MIC on/off on an encrypted link; refused with 12 on an arm
without capture), then the panel off. The session is reset first (a resident program stops).
Usage: python3 -I vm_check.py ADDR [--name L|R] [--out DIR]   (or SOCKET --emu: an emulated arm)
"""
import argparse
import json
import struct
import sys
import zlib
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "g1_firmware/emulator/tests"))
import g1host as g  # noqa: E402
import v5_tests as t  # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("addr", help="BLE address, or the emulator bridge socket with --emu")
    ap.add_argument("--emu", action="store_true")
    ap.add_argument("--name", default="arm")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()
    arm = g.Arm(g.EmuTransport(args.addr) if args.emu else g.BleTransport(args.addr), name=args.name)
    arm.setup()
    res = {"hello": arm.hello()}
    try:
        arm.reset_session(clear_arena=True)   # the programs expect a zero arena and registers
        arm.set_panel(0)
        arm.wait_idle(10)
        arm.sync_clock()
        base, code = t.listing("Code (176 bytes at 0xC000")
        table = [0] * 16
        table[0], table[1], table[12] = 0xC000, 0xC010, 0xC000
        t.example_setup(arm, code, table)
        arm.raw_packet(bytes.fromhex("01000100050000400000"))
        n1 = (arm.device_now() + 3000000) & g.M32
        arm.raw_packet(bytes.fromhex("0108010008") + struct.pack("<II", 12 * 60 + 34, n1))
        arm.wait_idle(20)
        assert arm.vm_start(0x40) == 0
        t.wait_tasks(arm, 1, 10)
        arm.wait(0.5)
        t.mirror_call(arm, 0xC000)
        t.panel_equal(arm, "clock_1234", res)
        tick = t.wait_tasks(arm, 2, 10)
        res["tick_late_ms"] = round(g.signed32(tick - n1) / 1000.0, 1)
        arm.wait(0.5)
        t.mirror_call(arm, 0xC010)
        t.panel_equal(arm, "clock_1235", res)
        arm.raw_packet(bytes.fromhex("0108010008") + struct.pack("<II", 23 * 60 + 59, (arm.device_now() + 60000000) & g.M32)
                       + bytes.fromhex("42f02800"))
        t.wait_tasks(arm, 3, 10)
        arm.wait(0.5)
        t.mirror_call(arm, 0xC000)
        t.panel_equal(arm, "clock_2359", res)
        res["vm_status_clock"] = {k: v for k, v in arm.vm_status().items() if k != "raw"}
        base, code = t.listing("Code (113 bytes at 0xC000")
        table = [0] * 16
        table[0], table[5] = 0xC000, 0xC000
        t.example_setup(arm, code, table)
        arm.raw_packet(bytes.fromhex("01000100040000003f"))
        arm.wait_idle(20)
        assert arm.vm_start(0x40) == 0
        t.wait_tasks(arm, 1, 10)
        arm.wait(0.5)
        pct = arm.props("BATT_PCT")["BATT_PCT"]
        arm.mirror.set_prop(g.PROPS["BATT_PCT"], pct)
        t.mirror_call(arm, 0xC000)
        t.panel_equal(arm, f"battery_{pct}", res)
        arm.vm_stop()
        arm.reset_session(clear_arena=True)
        k = arm.k
        tt = (arm.device_now() + 500000) & g.M32
        arm.write(4 * 420, struct.pack("<I", tt))
        n0 = len(arm.notifies)
        t.program(arm, {0: g.Code().ins("SYS_WRITE", k(0x22), 420).ins("END"),
                        1: g.Code().ins("SYS_READ", 8, k(0)).ins("NOTIFY", 8, 420).ins("END")})
        arm.run_until(lambda: len(arm.notifies) > n0, 10, "TIMER notify")
        res["timer_late_ms"] = round(g.signed32(arm.notifies[-1]["a"] - tt) / 1000.0, 3)
        arm.vm_stop()
        x = 4 if res["hello"]["role"] == "L" else 620

        def indicator(level):
            """PANEL_READ of rows 4-19 equals the mirror's panel with the 16 x 16 block at `level`."""
            p = bytearray(arm.mirror.panel)
            for y in range(4, 20):
                p[y * 640 + x:y * 640 + x + 16] = bytes([level]) * 16
            rows = b"".join(bytes((p[y * 640 + i] << 4) | p[y * 640 + i + 1] for i in range(0, 640, 2)) for y in range(4, 20))
            r = arm.ctrl("PANEL_READ", struct.pack("<HH", 4, 16), timeout=5.0)
            return r["status"] == 0 and struct.unpack_from("<I", r["body"])[0] == zlib.crc32(rows)

        res["mic_on"] = arm.mic(True)
        if res["mic_on"]["status"] == 12 and args.emu:
            res["mic_on"]["note"] = "the emulator's central cannot encrypt"
        arm.wait(0.5)
        res["mic_indicator_shown"] = indicator(15)
        res["mic_off"] = arm.mic(False)
        arm.wait(0.5)
        res["mic_indicator_cleared"] = indicator(0)
        arm.set_panel(0)
        arm.wait_idle(10)
        res["hello_end"] = {k_: v for k_, v in arm.hello().items() if k_ in ("panel_on", "epoch", "boot_count")}
        if res["hello"].get("mic"):
            mic_ok = res["mic_on"].get("status") == 0 and res["mic_indicator_shown"]
        else:   # an arm without capture (the left) refuses MIC with 12
            mic_ok = res["mic_on"].get("status") == 12 and not res["mic_indicator_shown"]
        res["pass"] = (all(v.get("match") for k_, v in res.items() if k_.startswith(("clock", "battery_")))
                       and mic_ok and res["mic_indicator_cleared"] and not res["hello_end"]["panel_on"])
    finally:
        print(json.dumps(res, indent=1, default=lambda o: o.hex() if isinstance(o, bytes) else str(o)))
        if args.out:
            Path(args.out).mkdir(parents=True, exist_ok=True)
            Path(args.out, "vm_check.json").write_text(json.dumps(res, indent=1, default=str))
        arm.tr.close()


if __name__ == "__main__":
    main()
