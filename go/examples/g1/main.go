// Command g1 puts notifications on the glasses.
//
//	g1 mirror [flags]                  the desktop's notifications on the glasses
//	g1 notify [flags] TITLE [BODY]     one message now
//	g1 assets [flags] list|put|forget  the arms' asset stores
//
// The mirror watches the session bus for org.freedesktop.Notifications.Notify
// calls as a D-Bus monitor (BecomeMonitor): the desktop's own notification
// daemon goes on as before, with its popups, history, actions and
// do-not-disturb, and the mirror sees every notification sent to it. When
// no daemon runs or can start (as on a machine without its graphical session,
// where a Notify call would hang and reach no monitor), or with -serve, it is
// the daemon itself, and it follows the daemon leaving or coming. The glasses
// run a VM program on each arm that keeps the last notifications: a new one
// pops up for a few seconds with its app's icon (Pebble's), half a second
// after it arrived (one closed or replaced meanwhile never shows), the last
// ones show while the head is tilted up, and the panel is off otherwise (in
// standby with -standby: faster to wake, about 1 mA more), also with the host
// away.
//
// g1 notify hands its message to a running mirror (a Unix socket), or else
// connects to the glasses itself, shows it and leaves.
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  g1 mirror [flags]              show the desktop's notifications on the glasses
  g1 notify [flags] TITLE [BODY] show one message now (through a running mirror, or directly)
  g1 assets [flags] list | put FILE | forget ID   the arms' asset stores (fonts and other data in flash)

g1 mirror -h, g1 notify -h and g1 assets -h list their flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "mirror":
		err = mirrorMain(os.Args[2:])
	case "notify":
		err = notifyMain(os.Args[2:])
	case "assets":
		err = assetsMain(os.Args[2:])
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "g1:", err)
		os.Exit(1)
	}
}
