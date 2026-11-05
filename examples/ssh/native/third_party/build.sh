#!/bin/sh
# The app's third-party code at pinned versions, for Android (arm64-v8a) and
# the host (tests): OpenSSL's libcrypto, protobuf's lite runtime and protoc,
# and mosh's sources (compiled by mosh.cmake).
#   native/third_party/build.sh [OUT]     default: native/third_party/out
# Needs curl, sha256sum, cmake, ninja, make, perl, a host C++ compiler and the
# NDK: ANDROID_NDK_ROOT, or ANDROID_HOME/ndk/27.3.13750724.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "${1:-$here/out}"
out=$(cd "${1:-$here/out}" && pwd)
ndk=${ANDROID_NDK_ROOT:-${ANDROID_HOME:-$HOME/Android/Sdk}/ndk/27.3.13750724}
api=31
jobs=$(nproc)

fetch() { # name url sha256: download into a new directory, check, unpack
  dir=$out/downloads/$1
  file=$dir/$(basename "$2")
  if [ ! -f "$file" ]; then
    rm -rf "$dir"
    mkdir -p "$dir"
    curl -fsSL -o "$file.part" "$2"
    mv "$file.part" "$file"
  fi
  echo "$3  $file" | sha256sum -c --quiet -
  if [ ! -d "$out/src/$1" ]; then
    rm -rf "$out/src/$1.tmp"
    mkdir -p "$out/src/$1.tmp"
    tar -xzf "$file" -C "$out/src/$1.tmp" --strip-components=1
    mv "$out/src/$1.tmp" "$out/src/$1"
  fi
}

fetch mosh-1.4.0 https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz \
  872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd
fetch openssl-3.5.9 https://github.com/openssl/openssl/releases/download/openssl-3.5.9/openssl-3.5.9.tar.gz \
  603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a
fetch protobuf-21.12 https://github.com/protocolbuffers/protobuf/releases/download/v21.12/protobuf-cpp-3.21.12.tar.gz \
  4eab9b524aa5913c6fffb20b2a8abf5ef7f95a80bc0701f3a6dbb4c607f73460

protobuf() { # build name, install prefix, extra CMake arguments
  name=$1
  prefix=$2
  shift 2
  [ -f "$prefix/lib/libprotobuf-lite.a" ] && return
  cmake -S "$out/src/protobuf-21.12" -B "$out/build/$name" -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$prefix" -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
    -Dprotobuf_BUILD_TESTS=OFF -Dprotobuf_WITH_ZLIB=OFF -Dprotobuf_BUILD_SHARED_LIBS=OFF "$@" >/dev/null
  cmake --build "$out/build/$name" -j "$jobs" >/dev/null
  cmake --install "$out/build/$name" >/dev/null
}

protobuf protobuf-host "$out/host"
protobuf protobuf-android "$out/android-arm64" -Dprotobuf_BUILD_PROTOC_BINARIES=OFF \
  -DCMAKE_TOOLCHAIN_FILE="$ndk/build/cmake/android.toolchain.cmake" -DANDROID_ABI=arm64-v8a \
  -DANDROID_PLATFORM=android-$api

if [ ! -f "$out/android-arm64/lib/libcrypto.a" ]; then
  mkdir -p "$out/build/openssl-android"
  (
    cd "$out/build/openssl-android"
    export ANDROID_NDK_ROOT="$ndk"
    export PATH="$ndk/toolchains/llvm/prebuilt/linux-x86_64/bin:$PATH"
    "$out/src/openssl-3.5.9/Configure" android-arm64 -D__ANDROID_API__=$api no-shared no-ssl3 no-comp \
      no-engine no-dso no-tests no-apps no-docs --prefix="$out/android-arm64" --libdir=lib >/dev/null
    make -j "$jobs" build_libs >/dev/null
    make install_dev >/dev/null
  )
fi

mkdir -p "$out/licenses"
cp "$out/src/mosh-1.4.0/COPYING" "$out/licenses/mosh.txt"
cp "$out/src/openssl-3.5.9/LICENSE.txt" "$out/licenses/openssl.txt"
cp "$out/src/protobuf-21.12/LICENSE" "$out/licenses/protobuf.txt"
echo "third-party code in $out"
