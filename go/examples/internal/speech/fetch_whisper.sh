#!/bin/sh
# Fetches whisper.cpp 1.9.5 (MIT), checks its SHA-256 and builds its static libraries into DIR, with
# DIR/lib/pkgconfig/whisper-static.pc for the speech demos (go build -tags whisper).
# Usage: fetch_whisper.sh DIR
set -eu
[ $# -eq 1 ] || { echo "usage: fetch_whisper.sh DIR" >&2; exit 2; }
VERSION=1.9.5
SHA=ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720
mkdir -p "$1"
DIR=$(cd "$1" && pwd -P)
[ -f "$DIR/lib/pkgconfig/whisper-static.pc" ] && { echo "whisper.cpp $VERSION already in $DIR"; exit 0; }
rm -rf "$DIR/download" "$DIR/src" "$DIR/build"
mkdir "$DIR/download" "$DIR/src"
NAME=whisper.cpp-$VERSION.tar.gz
curl -fL --proto '=https' -o "$DIR/download/$NAME" "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v$VERSION.tar.gz"
echo "$SHA  $DIR/download/$NAME" | sha256sum -c -
tar xzf "$DIR/download/$NAME" -C "$DIR/src" --strip-components=1
cmake -S "$DIR/src" -B "$DIR/build" -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DIR" -DCMAKE_INSTALL_LIBDIR=lib \
	-DBUILD_SHARED_LIBS=OFF -DWHISPER_BUILD_EXAMPLES=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF
cmake --build "$DIR/build" -j "$(nproc)"
cmake --install "$DIR/build"
cat > "$DIR/lib/pkgconfig/whisper-static.pc" <<PC
prefix=$DIR
libdir=\${prefix}/lib
includedir=\${prefix}/include

Name: whisper-static
Description: whisper.cpp $VERSION, static libraries
Version: $VERSION
Libs: -L\${libdir} -Wl,--start-group -lwhisper -lggml -lggml-base -lggml-cpu -Wl,--end-group -lgomp -lstdc++ -lm -lpthread
Cflags: -I\${includedir}
PC
echo "whisper.cpp $VERSION: $DIR (PKG_CONFIG_PATH=$DIR/lib/pkgconfig)"
