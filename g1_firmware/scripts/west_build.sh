#!/bin/sh
# One NCS v2.5.1 build for the nRF5340 app core. Runs inside the g1-ncs container (scripts/box).
# Usage: west_build.sh APP_DIR BUILD_DIR [-- cmake args...]
set -eu
APP=$1
OUT=$2
shift 2
NCS=${G1_WORK:?run through scripts/box}/ncs
. "$NCS/.venv/bin/activate"
export ZEPHYR_SDK_INSTALL_DIR=$NCS/zephyr-sdk-0.16.5-1
export ZEPHYR_TOOLCHAIN_VARIANT=zephyr
export CCACHE_DISABLE=1
cd "$NCS/ws"
west build -p always -b nrf5340dk_nrf5340_cpuapp -d "$OUT" "$APP" "$@"
