#!/bin/sh
# The nRF5340 DK as a second Bluetooth controller for the PC (dk_adapter.sh): app core = Zephyr's hci_uart
# (H:4 over the J-Link VCOM, 1 Mbaud, RTS/CTS), net core = hci_rpmsg + SoftDevice Controller, NCS v2.5.1.
# Builds in the firmware's g1-ncs container (../../g1_firmware/scripts/setup.sh build first).
# Usage: sh dk_hci/build.sh  ->  $G1_WORK/build/dk-hci/dk_hci_app.hex, dk_hci_net.hex, SHA256SUMS
set -eu
HERE=$(dirname "$(readlink -f "$0")")
FW=$(dirname "$(dirname "$HERE")")/g1_firmware
WORK=${G1_WORK:-$FW/work}
WORK=$(cd "$WORK" && pwd -P)
OUT=$WORK/build/dk-hci
LOG=$WORK/build/logs/build-dk-hci.log
mkdir -p "$WORK/build/logs"
if ! G1_WORK=$WORK BOXIMAGE=g1-ncs "$FW/scripts/box" sh "$FW/scripts/west_build.sh" "$HERE" "$OUT" > "$LOG" 2>&1; then
	echo "dk-hci FAILED ($LOG)"
	exit 1
fi
cp "$OUT/zephyr/zephyr.hex" "$OUT/dk_hci_app.hex"
cp "$OUT/hci_rpmsg/zephyr/zephyr.hex" "$OUT/dk_hci_net.hex"
cd "$OUT"
sha256sum dk_hci_app.hex dk_hci_net.hex > SHA256SUMS
cat SHA256SUMS
