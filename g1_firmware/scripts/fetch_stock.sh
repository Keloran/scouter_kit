#!/bin/sh
# Downloads Even's official firmware VERSION (default 1.6.6) from Even's CDN, checks the zip and its files
# against stock_images.txt and unpacks them into $G1_WORK/firmware/VERSION/; also writes
# $G1_WORK/fw/VERSION_net_raw.bin (the net-core image without its 512-byte header, for the emulator) and
# the parts of the images that g1v0 embeds, each checked by its own SHA-256: $G1_WORK/fw/1.6.6_aw9320x_table.bin,
# the TouchBar controller's 44-entry configuration table that stock loads into it (review-v5.md §8.1).
# G1_STOCK_MIRROR=DIR takes the zip from DIR instead of the CDN (same file name).
# Usage: scripts/fetch_stock.sh [VERSION]
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
WORK=${G1_WORK:-$FW/work}
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd -P)
V=${1:-1.6.6}
LIST=$FW/stock_images.txt
url=$(awk -v v="$V" '$1 == v && $2 == "zip" {print $4}' "$LIST")
[ -n "$url" ] || { echo "unknown version $V (see $LIST)"; exit 1; }
name=$(basename "$url")
DIR=$WORK/firmware/$V
mkdir -p "$DIR" "$WORK/fw"
if [ -n "${G1_STOCK_MIRROR:-}" ]; then
	cp "$G1_STOCK_MIRROR/$name" "$DIR/$name.part"
else
	curl -fL --proto '=https' -o "$DIR/$name.part" "$url"
fi
python3 -I - "$LIST" "$V" "$DIR" "$name" "$WORK/fw/${V}_net_raw.bin" <<'EOF'
import hashlib
import sys
import zipfile
from pathlib import Path

lst, v, d, name, raw = sys.argv[1:]
rows = [l.split() for l in open(lst) if l.strip() and not l.startswith("#")]
want = {f: h for ver, f, h, *_ in rows if ver == v}
parts = {f: rest[0] for ver, f, h, *rest in rows if ver == v and rest and "@" in rest[0]}
d = Path(d)


def check(label, data):
    got = hashlib.sha256(data).hexdigest()
    if got != want[label]:
        sys.exit(f"{label}: SHA-256 {got}, expected {want[label]}")


check("zip", (d / f"{name}.part").read_bytes())
(d / f"{name}.part").rename(d / name)
with zipfile.ZipFile(d / name) as z:
    for f in ("app_update.bin", "net_core_app_update.bin", "manifest.json"):
        data = z.read(f)
        check(f, data)
        (d / f).write_bytes(data)
Path(raw).write_bytes((d / "net_core_app_update.bin").read_bytes()[0x200:])
for name, where in parts.items():
    f, span = where.split("@")
    off, n = (int(x, 0) for x in span.split("+"))
    data = (d / f).read_bytes()[off:off + n]
    check(name, data)
    out = Path(raw).with_name(f"{v}_{name}.bin")
    out.write_bytes(data)
    print(f"{name}: {out} (checked)")
print(f"Even firmware {v}: {d} (checked), {raw}")
EOF
