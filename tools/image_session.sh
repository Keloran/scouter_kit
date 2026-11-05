#!/bin/sh
# One timed session of the Go `image` example through one BlueZ adapter, with a btmon capture of that
# adapter and the per-arm upload rate (upload_rate.py).
# Usage: sh image_session.sh hci0|hci1 NAME IMAGE [image flags...]  ->  $G1_LOG/NAME.btsnoop, NAME.txt (default logs/)
#   e.g. sh image_session.sh hci1 one-arm picture.gif -left none -right ADDR
set -eu
HERE=$(dirname "$(readlink -f "$0")")
ADAPTER=$1
NAME=$2
IMAGE=$3
shift 3
mkdir -p "${G1_LOG:-logs}"
LOG=$(cd "${G1_LOG:-logs}" && pwd)
BIN=${TMPDIR:-/tmp}/scouterkit-image-session/image
mkdir -p "$(dirname "$BIN")"
# Built once, so every session of a comparison runs the same binary (delete it to rebuild).
[ -x "$BIN" ] || (cd "$HERE/../go" && go build -a -o "$BIN" ./examples/image)
sha256sum "$BIN" | cut -c1-16
sudo sh -c "setsid btmon -i $ADAPTER -w $LOG/$NAME.btsnoop < /dev/null > /dev/null 2>&1 &"
sleep 1
rc=0
"$BIN" -adapter "$ADAPTER" -log "$@" "$IMAGE" > "$LOG/$NAME.txt" 2>&1 || rc=$?
sleep 1
sudo pkill -INT -f "^btmon -i $ADAPTER -w $LOG/$NAME.btsnoop"
sleep 1
echo "$NAME ($ADAPTER), exit $rc:"
grep -E 'picture at|panel:|adapter /|interval|PHY|resent|link down|problem|failed' "$LOG/$NAME.txt" | sed 's/^/  /'
"${PYTHON:-python3}" -I "$HERE/upload_rate.py" "$LOG/$NAME.btsnoop"
