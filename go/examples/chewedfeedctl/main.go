//go:build linux

// Command chewedfeedctl talks to a running chewedfeed over its private
// control socket:
//
//	chewedfeedctl [-socket PATH] status|release|resume|pin on|pin off
//
// release lets the glasses go (no phone needed) and keeps them released until
// resume. Exit status: 0 done, 3 still in progress (see status), 1 otherwise.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"scouterkit/examples/internal/localcontrol"
)

func main() {
	socket := flag.String("socket", localcontrol.DefaultPath(), "control socket of the running chewedfeed")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: chewedfeedctl [-socket PATH] status|release|resume|pin on|pin off")
		flag.PrintDefaults()
	}
	flag.Parse()
	req, ok := request(flag.Args())
	if !ok {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localcontrol.CommandTimeout+5*time.Second)
	defer cancel()
	resp, err := localcontrol.Do(ctx, *socket, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chewedfeedctl:", err)
		os.Exit(1)
	}
	if st := resp.Status; st != nil {
		fmt.Printf("owner: %s (%s)\neligible: %t\nreleased: %t\npinned: %t\narms: left %s, right %s\n",
			st.Owner, st.Phase, st.Eligible, st.Released, st.Pinned, ready(st.Left), ready(st.Right))
		if st.Reason != "" {
			fmt.Printf("reason: %s\n", st.Reason)
		}
	}
	switch resp.Result {
	case localcontrol.ResultDone:
	case localcontrol.ResultPending:
		fmt.Println("in progress; check status")
		os.Exit(3)
	default:
		fmt.Fprintf(os.Stderr, "chewedfeedctl: %s: %s\n", resp.Result, resp.Error)
		os.Exit(1)
	}
}

func request(args []string) (localcontrol.Request, bool) {
	switch {
	case len(args) == 1 && (args[0] == localcontrol.CmdStatus || args[0] == localcontrol.CmdRelease || args[0] == localcontrol.CmdResume):
		return localcontrol.Request{Command: args[0]}, true
	case len(args) == 2 && args[0] == localcontrol.CmdPin && (args[1] == "on" || args[1] == "off"):
		return localcontrol.Request{Command: localcontrol.CmdPin, Pin: args[1] == "on"}, true
	}
	return localcontrol.Request{}, false
}

func ready(ok bool) string {
	if ok {
		return "ready"
	}
	return "down"
}
