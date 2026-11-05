#!/bin/sh
# Fetches Renode 1.16.1 (portable .NET build) into $G1_WORK/renode/ and checks its SHA-256.
# G1_RENODE_MIRROR=DIR takes the tarball from DIR instead of GitHub.
# Usage: scripts/fetch_renode.sh
set -eu
FW=$(dirname "$(dirname "$(readlink -f "$0")")")
WORK=${G1_WORK:-$FW/work}
mkdir -p "$WORK/dl" "$WORK/renode"
WORK=$(cd "$WORK" && pwd -P)
NAME=renode-1.16.1.linux-portable-dotnet.tar.gz
SHA=00e113cdbd0f5354cf2f64bbe3f5a070d8958409542fca66e45ac97d982938c0
[ -x "$WORK/renode/renode_1.16.1-dotnet_portable/renode" ] && { echo "Renode 1.16.1 already in $WORK/renode"; exit 0; }
if [ -n "${G1_RENODE_MIRROR:-}" ]; then
	cp "$G1_RENODE_MIRROR/$NAME" "$WORK/dl/$NAME"
else
	curl -fL --proto '=https' -o "$WORK/dl/$NAME" "https://github.com/renode/renode/releases/download/v1.16.1/$NAME"
fi
echo "$SHA  $WORK/dl/$NAME" | sha256sum -c -
tar xzf "$WORK/dl/$NAME" -C "$WORK/renode"
echo "Renode 1.16.1: $WORK/renode/renode_1.16.1-dotnet_portable"
