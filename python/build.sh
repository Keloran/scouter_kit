#!/usr/bin/env bash
# Builds the package's two shared objects from ../src:
#   scouterkit/libscouterkit.so           the glasses, through BlueZ (sd-bus from libsystemd)
#   scouterkit/libscouterkit_emulator.so  two emulated arms (-DSCOUTER_G1_EMULATOR)
# Both include the microphone (-DSCOUTER_G1_MIC, the vendored liblc3) and export only the C layer.
# Same sources, compiler and flags give byte-identical files.
set -euo pipefail
export LC_ALL=C

here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
cc=${CC:-gcc}
cxx=${CXX:-g++}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$root"

flags=(-O2 -fPIC -ffile-prefix-map="$root/=" -DSCOUTER_G1_MIC)
lc3=(-DLC3_PLUS=0 -DLC3_PLUS_HR=0 -Ithird_party/liblc3/include)
printf '{ global: sk_*; local: *; };\n' > "$tmp/exports.map"

for f in third_party/liblc3/src/*.c; do
  "$cc" -std=c11 "${flags[@]}" "${lc3[@]}" -c "$f" -o "$tmp/lc3_$(basename "$f" .c).o"
done

# build OUTPUT "LIBS" CFLAGS...
build() {
  local out=$1 libs=$2 obj=$tmp/$(basename "$1" .so)
  shift 2
  mkdir -p "$obj"
  for f in src/vm/vm.c src/vm/vm_gfx.c; do
    "$cc" -std=c11 "${flags[@]}" "$@" -c "$f" -o "$obj/$(basename "$f" .c).o"
  done
  for f in src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp src/c/scouter_kit_c.cpp python/csrc/departure_mono.cpp; do
    "$cxx" -std=c++20 "${flags[@]}" "$@" -c "$f" -o "$obj/$(basename "$f" .cpp).o"
  done
  # shellcheck disable=SC2086
  "$cxx" -shared -Wl,-z,defs -Wl,--version-script="$tmp/exports.map" -o "$out" \
    "$obj"/*.o "$tmp"/lc3_*.o $libs -lpthread -lm
}

# shellcheck disable=SC2046
build "$here/scouterkit/libscouterkit.so" "$(pkg-config --libs libsystemd)" $(pkg-config --cflags libsystemd)
build "$here/scouterkit/libscouterkit_emulator.so" "" -DSCOUTER_G1_EMULATOR
cd "$here" && sha256sum scouterkit/libscouterkit.so scouterkit/libscouterkit_emulator.so
