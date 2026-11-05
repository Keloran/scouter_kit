#!/bin/sh
# An nRF5340 DK running the dk_hci controller image (dk_hci/, built by dk_hci/build.sh) as a second BlueZ
# adapter. The DK is reached only through its J-Link serial number's by-id paths; no other port is opened.
#   DK_SN=SERIAL sh dk_adapter.sh attach   btattach (H:4, 1 Mbaud, RTS/CTS), event length, net-core console log
#   DK_SN=SERIAL sh dk_adapter.sh tune     send the event length again (needed after every power-on of the adapter)
#   DK_SN=SERIAL sh dk_adapter.sh detach   stop btattach: the adapter leaves BlueZ
#   DK_SN=SERIAL sh dk_adapter.sh status
# SERIAL: the J-Link's serial number (ls /dev/serial/by-id). Logs go to $G1_LOG/dk-adapter (default logs/).
# While attached, a bare `bluetoothctl` may use the DK as its default controller: other tools must select
# their adapter explicitly.
set -eu
SN=${DK_SN:?set DK_SN to the J-Link serial number of the DK}
HCI_TTY=/dev/serial/by-id/usb-SEGGER_J-Link_${SN}-if02
NET_TTY=/dev/serial/by-id/usb-SEGGER_J-Link_${SN}-if00
LOG=${G1_LOG:-logs}/dk-adapter
EVENT_LEN="ac 0d 00 00" # 3500 us, little-endian: SDC vendor command Event Length Set (0xfd05)

attach_pid() { pgrep -f "^btattach -B $HCI_TTY" || true; }
console_pid() { pgrep -f "^cat $NET_TTY" || true; }
hci() {
	tty=$(basename "$(readlink -f "$HCI_TTY")")
	for d in /sys/class/bluetooth/hci*; do
		[ -e "$d/device" ] && [ "$(basename "$(readlink -f "$d/device")")" = "$tty" ] && basename "$d" && return
	done
	true
}
tune() {
	h=$(hci)
	[ -n "$h" ] || { echo "no DK adapter"; exit 1; }
	# shellcheck disable=SC2086
	sudo hcitool -i "$h" cmd 0x3f 0x105 $EVENT_LEN | tail -1
}

mkdir -p "$LOG"
LOG=$(cd "$LOG" && pwd)
case "${1:-status}" in
attach)
	if [ -z "$(console_pid)" ]; then
		stty -F "$NET_TTY" 115200 raw -echo -hupcl
		setsid sh -c "exec cat $NET_TTY >> $LOG/netcore-console.log" < /dev/null > /dev/null 2>&1 &
	fi
	if [ -z "$(attach_pid)" ]; then
		sudo sh -c "setsid btattach -B $HCI_TTY -S 1000000 -P h4 < /dev/null >> $LOG/btattach.log 2>&1 &"
	fi
	i=0
	until [ -n "$(hci)" ] && hciconfig "$(hci)" | grep -q 'UP RUNNING'; do
		i=$((i + 1))
		[ $i -gt 40 ] && { echo "the DK did not come up (see $LOG/btattach.log)"; exit 1; }
		sleep 0.25
	done
	sleep 1 # bluetoothd finishes its setup (it powers the adapter on, which resets it)
	tune
	hciconfig "$(hci)" | head -3
	;;
tune) tune ;;
detach)
	p=$(attach_pid)
	[ -n "$p" ] && sudo kill $p
	p=$(console_pid)
	[ -n "$p" ] && kill $p
	i=0
	while [ -n "$(hci)" ] && [ $i -lt 40 ]; do # the kernel removes the adapter shortly after btattach ends
		i=$((i + 1))
		sleep 0.25
	done
	[ -z "$(hci)" ] && echo detached || { echo "the DK adapter is still there"; exit 1; }
	;;
status)
	h=$(hci)
	if [ -n "$h" ]; then hciconfig "$h" | head -3; else echo "not attached"; fi
	;;
*) echo "usage: $0 attach|tune|detach|status"; exit 2 ;;
esac
