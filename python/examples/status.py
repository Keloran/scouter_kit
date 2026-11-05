"""Prints each arm's link, firmware, battery, panel and program without disturbing the arms:
the sessions are kept (keep_sessions), so a program on them, such as the g1 notification
program, runs on.

    python examples/status.py [--full] [--emulator]
"""

from __future__ import annotations

import sys

import common
import scouterkit as sk

PROGRAM_STATES = ("stopped", "running", "halted")
FULL_PROPS = (sk.Prop.TIME_US, sk.Prop.LINK, sk.Prop.PANEL, sk.Prop.BRIGHTNESS, sk.Prop.TIMER,
              sk.Prop.PITCH_LO, sk.Prop.PITCH_HI, sk.Prop.IMU_RATE, sk.Prop.IN_CASE, sk.Prop.CHARGE,
              sk.Prop.BATT_MA, sk.Prop.AUTO_BRIGHTNESS)


def shown(prop: sk.Prop, value: int) -> str:
    if value == sk.UNKNOWN:
        return "unknown"
    if prop in (sk.Prop.PITCH_LO, sk.Prop.PITCH_HI, sk.Prop.BATT_MA) and value >= 2**31:
        return str(value - 2**32)
    return str(value)


def main(argv: list[str] | None = None) -> int:
    p = common.parser(__doc__.splitlines()[0])
    p.add_argument("--full", action="store_true",
                   help="also every field of the arm's and the program's state, and more properties")
    args = p.parse_args(argv)
    with common.connect(args, keep_sessions=True):
        for arm in common.present():
            s = arm.state()
            battery = arm.battery()
            vm = arm.vm_status()
            panel = arm.prop(sk.Prop.PANEL)
            charging = ", charging" if battery.charging else ""
            print(f"{arm.name}: firmware {s.firmware}, link {s.interval_us / 1000:g} ms, "
                  f"in case {s.in_case}, battery {battery.percent} % {battery.millivolts} mV{charging}, "
                  f"panel {'on' if panel else 'off'}, program {PROGRAM_STATES[vm.state]} "
                  f"(table 0x{vm.table:05x}, {vm.tasks} tasks)")
            if args.full:
                values = arm.props(*FULL_PROPS)
                print(f"  {s}\n  {battery}\n  {vm}")
                print("  " + ", ".join(f"{prop.name} {shown(prop, v)}" for prop, v in zip(FULL_PROPS, values)))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, sk.ScouterKitError) as e:
        sys.exit(f"status: {e}")
