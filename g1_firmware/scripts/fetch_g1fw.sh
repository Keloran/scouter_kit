#!/bin/sh
# Fetches FreedomCoder-dev/g1-fw at the pinned commit into $G1_WORK/g1-fw: its Renode models, nRF5340
# platform, FICR/UICR images and settings-seed generator, which the emulator uses. g1-fw has no licence,
# so none of it is redistributed here. Checks the files used (emulator/g1-fw.sha256) and writes
# $G1_WORK/models/ with this project's changes to seven of its models (emulator/models/patches/*.ed:
# ed scripts holding only the changed lines), checked against patches/SHA256SUMS.
# G1FW_URL=URL_OR_PATH clones from elsewhere (e.g. an existing local clone).
# Usage: scripts/fetch_g1fw.sh
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
WORK=${G1_WORK:-$FW/work}
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd -P)
REV=b8ec262b502907ce0ff121411b2cfc2eea9b7322
D=$WORK/g1-fw
[ -d "$D/.git" ] || git clone -q "${G1FW_URL:-https://github.com/FreedomCoder-dev/g1-fw}" "$D"
[ "$(git -C "$D" rev-parse HEAD)" = "$REV" ] || git -C "$D" -c advice.detachedHead=false checkout -q --detach "$REV"
[ "$(git -C "$D" rev-parse HEAD)" = "$REV" ] || { echo "g1-fw: not at $REV"; exit 1; }
(cd "$D" && sha256sum --quiet -c "$FW/emulator/g1-fw.sha256")
mkdir -p "$WORK/models"
for p in "$FW"/emulator/models/patches/*.ed; do
	m=$(basename "$p" .ed)
	python3 -I "$FW/scripts/apply_ed.py" "$D/models/$m" "$p" "$WORK/models/$m"
done
(cd "$WORK/models" && sha256sum --quiet -c "$FW/emulator/models/patches/SHA256SUMS")
echo "g1-fw $REV: $D, patched models in $WORK/models"
