# ScouterKit tools

Testing tools for G1 arms running the ScouterKit firmware g1v0 0.5.x (protocol 5,
[../g1_firmware/docs/VM.md](../g1_firmware/docs/VM.md)). Linux with BlueZ; Python 3.10 or newer.

```
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
```

`g1host.py` builds two small shared libraries the first time it needs them (the VM core with
`g1mirror.c`, and liblc3), so a C compiler (`cc`) is needed; they are cached in `~/.cache/g1host`.

Most tools take `--ble ADDR` (an arm's Bluetooth address under g1v0) and, where it makes sense,
`--emu SOCKET` for an emulated arm (`../g1_firmware/scripts/bridge_start.sh`). An arm accepts commands
from an unbonded host only while it is in the charging case; out of the case the link must be bonded
and encrypted (pair the PC with the arm in the case first, see the firmware README). g1v0 0.5.32–0.5.58
holds the panel off in the case; since 0.5.60 the panel works there as anywhere (a program that keeps it off
in the case or folded uses the library's `PanelPolicy`).

| Tool | What it does |
|---|---|
| `g1host.py` | The reference client of protocol 5: `hello`, `status`, `battery`, `props`, `imu`, `power`, `grid --verify`, `drawtest`, `linktest`, `vm-status`, `mic on/off`, `brightness`, `panel on/off/standby`, `touch-watch S` (the TouchBar's `TOUCH` and `WEAR` events for S seconds), `case-watch S` (the `CASE` and `WEAR` events: `IN_CASE`, the lid, the case's charger and battery), `light-watch S [--auto]` (the panel on, `LIGHT` and `BRIGHTNESS` every second), `gyro-watch S` (the gyroscope at 104 Hz, `GYR_*` and `PITCH`/`ROLL` five times a second), `peer-stats` (the arm-to-arm link's counters and signal value, `WORN`), `link`, `reboot`, `bonds` (0.5.58+: the arm's bonds, oldest first), the SMP commands `smp-list`, `smp-upload`, `smp-commit` (also against Even's firmware, `--hb 8`), and the diagnostics of test builds. It mirrors the arm with the VM core itself, so arena and panel compare byte for byte. `python3 -I g1host.py --ble ADDR hello` |
| `arm_pair.py` | Pairs (bonds) the PC with an arm running g1v0, in the case, with its own BlueZ agent that accepts only that arm: `bluetoothctl pairable on`, then `python3 -I arm_pair.py ADDR` |
| `g1mirror.c` | The mirror's platform around the VM core (`../src/vm`), loaded by `g1host.py` |
| `vm_check.py` | VM checks: VM.md §9.2's clock and §9.3's battery programs from the bytes printed in VM.md, each frame compared with the mirror by `PANEL_READ`; a TIMER handler's lateness; the microphone indicator |
| `asset_check.py` | The asset store's acceptance on one arm (0.5.56+): the fingerprints of the flash the store never writes, a font put (`--step`: single-stepped with the fingerprints after every flash call), a bitmap put with its step times while PING and a TIMER program run, the log checked, a reboot, a timed `ASSET_LOAD` restore, the fingerprints again |
| `draw_bench.py` | DRAW timing by the arm's own clock for a set of rectangle sizes |
| `peer_check.py` | The arm-to-arm link with both arms connected: values from the right arm's program to the left's and back (round trip, one-way time), two programs drawing together without the host (both panels read back), the left following the right's brightness: `python3 -I peer_check.py LEFT_ADDR RIGHT_ADDR` |
| `imu_check.py` | Accelerometer checks that need no movement (readings at rest, MOTION and TILT as events and as program triggers, CPU wake-ups) |
| `imu_cal.py` | The two-pose head-frame calibration, glasses worn: prints the per-arm axis map for the firmware |
| `mic_check.py` | Microphone check on the right arm: short bursts, indicator on, only aggregate numbers kept (frames, gaps, decoder status, level); no audio is stored or sent on |
| `btsum.py` | Summary of a btmon/btsnoop capture per connection (parameters, PHY, data length, security, ATT, disconnect reasons). `G1_ARMS=L=ADDR,R=ADDR` labels the arms |
| `upload_rate.py` | Per-arm upload rate of a ScouterKit session from a btsnoop capture |
| `image_session.sh` | One timed run of the Go `image` example (with `-log`) with a btmon capture and `upload_rate.py` |
| `case_serial_listen.py`, `case_log_text.py` | Passive logger of the charging case's debug output (USB serial, 115200 8N1): `python3 -I case_serial_listen.py --port /dev/ttyUSB0 --bauds 115200 --seconds 600 --log case.jsonl`, then `case_log_text.py case.jsonl` |
| `dk_adapter.sh`, `dk_hci/` | An nRF5340 DK as a second Bluetooth adapter (more upload bandwidth when both arms are driven): `sh dk_hci/build.sh` builds its controller firmware, `DK_SN=SERIAL sh dk_adapter.sh attach` attaches it |

Notes:
- The case logger only listens. Never write to the case's serial port: some byte sequences start the
  case's firmware updater.
- Tools that use the microphone keep its lease alive (`g1host.py` `mic_hold`); the arm switches it
  off 2 s after the last renewal. Mind the people around you: short bursts, nothing recorded.
- A tool that runs while BlueZ holds a link to the arm may fail to connect: BlueZ reconnects bonded
  devices by itself (`bluetoothctl disconnect ADDR` first).
