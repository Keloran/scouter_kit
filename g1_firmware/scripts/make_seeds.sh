#!/bin/sh
# The emulator's QSPI settings seeds (the provisioned settings record at 0x130000) in $G1_WORK/fw/seed/:
# g1-fw's generator (run in the g1-renode container), then the ESB pairing of a G1 (channel 34, addresses
# 0x41/0x42) and a test serial number. Checked against fixed SHA-256 sums. Needs scripts/fetch_g1fw.sh.
# Usage: scripts/make_seeds.sh
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
WORK=${G1_WORK:-$FW/work}
WORK=$(cd "$WORK" && pwd -P)
S=$WORK/fw/seed
mkdir -p "$S"
G1_WORK=$WORK "$FW/scripts/box" python3 -I g1-fw/scripts/gen_g1_seed.py
cp "$WORK/g1-fw/fw/g1_extstore_seed.bin" "$S/"
python3 -I "$FW/scripts/patch_seed.py" "$S/g1_extstore_seed.bin" "$S/g1_extstore_seed_paired34.bin" 34 0x41 0x42
python3 -I "$FW/scripts/seed_serial.py" "$S/g1_extstore_seed_paired34.bin" "$S/g1_extstore_seed_paired34_serial.bin" S110LTST000042 0x15
cd "$S"
sha256sum -c - <<EOF
eb33a3f999c321116167cb4fa2f9fcc57a121fd6e995cdcf35d692285700eb23  g1_extstore_seed.bin
db7ebfc6dec948c9d6a087fe3968d4f7209929ca431b96d593b399eecc9741e6  g1_extstore_seed_paired34.bin
36d95040acd8f840d28b406c4b7842783b25355244447388fc23e556d998acc0  g1_extstore_seed_paired34_serial.bin
EOF
