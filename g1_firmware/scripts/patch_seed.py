"""Usage: python3 -I patch_seed.py SEED OUT ESB_CHANNEL MASTER_ADDR SLAVE_ADDR"""
import sys
from pathlib import Path

# Offsets inside the 0x130000 sys-settings record (as stock's load_sys_setting reads it):
# +0x08 esb_channel -> device_info+2, +0x55 -> device_info+3 (ESB master addr), +0x56 -> device_info+4 (slave addr).
src, dst, channel, master, slave = sys.argv[1], sys.argv[2], *(int(v, 0) for v in sys.argv[3:6])
seed = bytearray(Path(src).read_bytes())
seed[0x08] = channel
seed[0x55] = master
seed[0x56] = slave
Path(dst).write_bytes(seed)
print(f"{dst}: channel {channel} master 0x{master:02x} slave 0x{slave:02x}")
