#!/bin/sh
# One-time setup of the work directory (G1_WORK, default g1_firmware/work).
#   build     the podman images (container/), NCS v2.5.1 and the Zephyr SDK (scripts/ncs_fetch.sh, with network
#             in the container), Even's 1.6.6 images (scripts/fetch_stock.sh: the build embeds its net core)
#   emulator  Renode 1.16.1, g1-fw's models (scripts/fetch_g1fw.sh), the QSPI seeds (scripts/make_seeds.sh)
#             and the replica of the G1's MCUboot (g1min/), checked against its known SHA-256
# Host packages (Debian): podman uidmap passt fuse-overlayfs git curl python3.
# Usage: scripts/setup.sh build|emulator|all
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
WORK=${G1_WORK:-$FW/work}
mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd -P)
export G1_WORK=$WORK
MCUBOOT_SHA=d4a73a3fe3c3826b21d67c80ae8a29d3772056f788b5ef9a576eac5af77ac8d3

build() {
	podman build -t g1-ncs -f "$FW/container/Containerfile.ncs" "$FW/container"
	podman build -t g1-renode -f "$FW/container/Containerfile.renode" "$FW/container"
	BOXIMAGE=g1-ncs BOXNET=pasta "$FW/scripts/box" sh "$FW/scripts/ncs_fetch.sh"
	sh "$FW/scripts/fetch_stock.sh" 1.6.6
}

emulator() {
	sh "$FW/scripts/fetch_renode.sh"
	sh "$FW/scripts/fetch_g1fw.sh"
	sh "$FW/scripts/make_seeds.sh"
	mkdir -p "$WORK/build/logs"
	BOXIMAGE=g1-ncs "$FW/scripts/box" sh "$FW/scripts/west_build.sh" "$FW/g1min" "$WORK/build/g1min" \
		> "$WORK/build/logs/build-g1min.log" 2>&1 || { echo "g1min build FAILED ($WORK/build/logs/build-g1min.log)"; exit 1; }
	echo "$MCUBOOT_SHA  $WORK/build/g1min/mcuboot/zephyr/zephyr.bin" | sha256sum -c - \
		|| echo "the replica MCUboot differs from the tested one: load-mcuboot.resc's erase hook may not fit"
}

case "${1:-}" in
build) build ;;
emulator) emulator ;;
all) build && emulator ;;
*) echo "usage: scripts/setup.sh build|emulator|all"; exit 2 ;;
esac
