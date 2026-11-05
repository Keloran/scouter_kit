package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"scouterkit"
)

// g1 assets list|put|forget: the asset store of both arms (VM.md §6.10).
func assetsMain(args []string) error {
	fs := flag.NewFlagSet("assets", flag.ExitOnError)
	left := fs.String("left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	right := fs.String("right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	adapter := fs.String("adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	typ := fs.Int("type", int(scouterkit.AssetRaw), "put: 1 font, 2 4-bpp bitmap, 3 1-bpp bitmap, 4 raw")
	log := fs.Bool("log", false, "the library logs to stderr")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: g1 assets [flags] list | put FILE | forget ID\n\nThe arms' asset stores: list what each arm holds, put a file on both arms (once: an arm that\nholds it already is skipped), or delete an asset by its ID (32 hex digits). list and forget keep a\nprogram that runs on the arms; put starts a new session.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	cmd := fs.Arg(0)
	if (cmd == "list" && fs.NArg() != 1) || ((cmd == "put" || cmd == "forget") && fs.NArg() != 2) ||
		(cmd != "list" && cmd != "put" && cmd != "forget") {
		fs.Usage()
		os.Exit(2)
	}
	var blob []byte
	var id scouterkit.AssetID
	switch cmd {
	case "put":
		b, err := os.ReadFile(fs.Arg(1))
		if err != nil {
			return err
		}
		blob = b
	case "forget":
		b, err := hex.DecodeString(strings.TrimSpace(fs.Arg(1)))
		if err != nil || len(b) != len(id) {
			return fmt.Errorf("an asset ID has 32 hex digits")
		}
		copy(id[:], b)
	}
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter, cfg.Log = *left, *right, *adapter, *log
	cfg.KeepSessions = cmd != "put"
	if err := scouterkit.Init(cfg); err != nil {
		return err
	}
	defer scouterkit.Deinit()
	switch cmd {
	case "put":
		if err := scouterkit.Store(blob, scouterkit.AssetType(*typ)); err != nil {
			return err
		}
		fmt.Printf("%s stored on both arms (%d bytes)\n", scouterkit.AssetIDOf(blob), len(blob))
		return nil
	case "forget":
		return scouterkit.Forget(id)
	}
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		if *left == "none" && arm == scouterkit.Left || *right == "none" && arm == scouterkit.Right {
			continue
		}
		list, err := scouterkit.Assets(arm)
		if err != nil {
			return err
		}
		fmt.Printf("%s arm: %d assets\n", arm, len(list))
		for _, a := range list {
			fmt.Printf("  %s  %7d bytes  type %d  at 0x%06x  crc %08x\n", a.ID, a.Size, a.Type, a.Offset, a.CRC)
		}
	}
	return nil
}
