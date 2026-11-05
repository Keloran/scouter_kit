#!/bin/sh
# Fetches sherpa-onnx 1.13.8 (Apache-2.0; with ONNX Runtime, MIT), its prebuilt shared libraries, C API header and
# tools for Linux x64, checks the SHA-256 and writes DIR/lib/pkgconfig/sherpa-onnx-c.pc for the speech demos
# (go build -tags sherpa).
# Usage: fetch_sherpa.sh DIR
set -eu
[ $# -eq 1 ] || { echo "usage: fetch_sherpa.sh DIR" >&2; exit 2; }
VERSION=1.13.8
SHA=c0bdb7907d3a74bba1d55d22bf4d9fa75586cf1530614ebe88a27b9118e015c4
mkdir -p "$1"
DIR=$(cd "$1" && pwd -P)
[ -f "$DIR/lib/pkgconfig/sherpa-onnx-c.pc" ] && { echo "sherpa-onnx $VERSION already in $DIR"; exit 0; }
rm -rf "$DIR/download" "$DIR/lib" "$DIR/include" "$DIR/bin"
mkdir "$DIR/download"
NAME=sherpa-onnx-v$VERSION-linux-x64-shared
curl -fsSL --proto '=https' -o "$DIR/download/$NAME.tar.bz2" \
	"https://github.com/k2-fsa/sherpa-onnx/releases/download/v$VERSION/$NAME.tar.bz2"
echo "$SHA  $DIR/download/$NAME.tar.bz2" | sha256sum -c -
tar xjf "$DIR/download/$NAME.tar.bz2" -C "$DIR" --strip-components=1
mkdir -p "$DIR/lib/pkgconfig"
cat > "$DIR/lib/pkgconfig/sherpa-onnx-c.pc" <<PC
prefix=$DIR
libdir=\${prefix}/lib
includedir=\${prefix}/include

Name: sherpa-onnx-c
Description: sherpa-onnx $VERSION C API, prebuilt
Version: $VERSION
Libs: -L\${libdir} -Wl,-rpath,\${libdir} -lsherpa-onnx-c-api
Cflags: -I\${includedir}
PC
echo "sherpa-onnx $VERSION: $DIR (PKG_CONFIG_PATH=$DIR/lib/pkgconfig)"
