#!/bin/sh
# Fetches NCS v2.5.1 (the subset g1v0 needs) and the Zephyr SDK 0.16.5-1 arm toolchain into $G1_WORK/ncs.
# Runs inside the g1-ncs container with network: BOXIMAGE=g1-ncs BOXNET=pasta scripts/box sh scripts/ncs_fetch.sh
# The Python packages come from scripts/ncs_requirements.txt (pinned, with their SHA-256); the checkout is
# checked against the git commits below.
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
NCS=${G1_WORK:?run through scripts/box}/ncs
SDK=https://github.com/zephyrproject-rtos/sdk-ng/releases/download/v0.16.5-1
mkdir -p "$NCS"
cd "$NCS"
[ -d .venv ] || python3 -m venv .venv
. .venv/bin/activate
pip install -q --require-hashes -r "$FW/scripts/ncs_requirements.txt"
if [ ! -d ws/.west ]; then
	west init -m https://github.com/nrfconnect/sdk-nrf --mr v2.5.1 ws
fi
cd ws
west config manifest.project-filter -- "-.*,+zephyr,+mcuboot,+mbedtls,+nrfxlib,+hal_nordic,+cmsis,+zcbor,+tinycrypt,+segger,+open-amp,+libmetal"
west update --narrow -o=--depth=1
while read -r sha path; do
	[ "$(git -C "$path" rev-parse HEAD)" = "$sha" ] || { echo "NCS: $path is not at $sha"; exit 1; }
done <<EOF
87355af5914e498f449b7a68bb5641031a7b8885 nrf
83980fe1679441be9b0e1db556a353f6118fe14f zephyr
edfe1e1465dbc698bf9a195816247913490ab391 bootloader/mcuboot
acea48fc8a5eb227033b55e6ec012731218e257f modules/crypto/mbedtls
3e9a49d2672ec01435ffbf0d788db6d95ef28de0 modules/crypto/tinycrypt
9d0191285956cef43daf411edc2f1a7788346def modules/debug/segger
1abf29132e608826752e2edd1f4799a065db4031 modules/hal/cmsis
b91611a6f47dd29fb24c46e5621e797557f80ec6 modules/hal/libmetal
9784731461018d3e983604698fbbed6af2bea801 modules/hal/nordic
42b7c577714b8f22ce82a901e19c1814af4609a8 modules/lib/open-amp
67fd8bb88d3136738661fa8bb5f9989103f4599e modules/lib/zcbor
ab72f33c86db7252dbf9a3ffec86c6b7fc6a9da7 nrfxlib
EOF
cd "$NCS"
if [ ! -d zephyr-sdk-0.16.5-1 ]; then
	mkdir -p dl
	cd dl
	wget -q "$SDK/zephyr-sdk-0.16.5-1_linux-x86_64_minimal.tar.xz" "$SDK/toolchain_linux-x86_64_arm-zephyr-eabi.tar.xz"
	sha256sum -c - <<EOF
f23836a1d35800ae7d98073f8f7c79166deeed753b5af927ef778764592d9675  zephyr-sdk-0.16.5-1_linux-x86_64_minimal.tar.xz
d6455a2fd4e0bc03091f255fb8f21853f76dd2a19f7d57cd038ead594d9d76e8  toolchain_linux-x86_64_arm-zephyr-eabi.tar.xz
EOF
	cd "$NCS"
	tar xf dl/zephyr-sdk-0.16.5-1_linux-x86_64_minimal.tar.xz
	tar xf dl/toolchain_linux-x86_64_arm-zephyr-eabi.tar.xz -C zephyr-sdk-0.16.5-1
fi
west --version
echo FETCH_DONE
