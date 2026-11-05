"""Usage: python3 -I seed_serial.py SEED OUT SERIAL [OFFSET=0x35]"""
import sys
from pathlib import Path

# Sys-settings record at QSPI 0x130000: +0x15 and +0x35 hold 16-byte ASCII IDs (load_sys_setting);
# +0x35 is the glasses SN that is_old_board() checks (chars 4..8 = "EGP0x" or "EGP1[0-3]" -> Vterm 4.2 V).
src, dst, serial = sys.argv[1], sys.argv[2], sys.argv[3].encode()
off = int(sys.argv[4], 0) if len(sys.argv) > 4 else 0x35
seed = bytearray(Path(src).read_bytes())
seed[off:off + 16] = serial[:16].ljust(16, b"\x00")
Path(dst).write_bytes(seed)
print(f"{dst}: serial {serial[:16]!r}")
