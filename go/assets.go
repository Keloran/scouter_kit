package scouterkit

/*
#include "scouter_kit_c.h"
*/
import "C"

import (
	"encoding/hex"
	"errors"
	"fmt"
	"unsafe"
)

// AssetID names an asset of the arms' stores (VM.md §6.10): the first 16
// bytes of the SHA-256 of its body.
type AssetID [16]byte

func (id AssetID) String() string { return hex.EncodeToString(id[:]) }

// AssetType is the tag an arm keeps with an asset; the arm does not use it.
type AssetType int

const (
	AssetFont    AssetType = C.SK_G1_ASSET_FONT    // a font blob (VM.md §4.2)
	AssetBitmap  AssetType = C.SK_G1_ASSET_BITMAP  // "SKB1", u16 w, h, stride, 0, 4-bpp rows
	AssetBitmap1 AssetType = C.SK_G1_ASSET_BITMAP1 // the same header, 1-bpp rows
	AssetRaw     AssetType = C.SK_G1_ASSET_RAW
)

// Asset is an entry of an arm's store.
type Asset struct {
	ID     AssetID
	Offset uint32 // in the arm's store
	Size   uint32
	CRC    uint32 // crc32 (zlib's) of the body
	Type   AssetType
}

// AssetIDOf returns the ID of a blob.
func AssetIDOf(blob []byte) AssetID {
	var id AssetID
	var p unsafe.Pointer
	if len(blob) > 0 {
		p = unsafe.Pointer(&blob[0])
	}
	C.sk_g1_asset_id(p, C.size_t(len(blob)), (*C.uint8_t)(unsafe.Pointer(&id[0])))
	return id
}

// Store puts the blob (1 ... 262144 bytes) on each arm that lacks it and
// returns once both hold it, about 1 s per 64 KB on an arm that needs it.
func Store(blob []byte, t AssetType) error {
	if len(blob) == 0 {
		return errors.New("scouterkit: empty asset")
	}
	var msg [256]C.char
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return ErrNotInitialized
	}
	if C.sk_g1_store(unsafe.Pointer(&blob[0]), C.size_t(len(blob)), C.int(t), &msg[0], C.size_t(len(msg))) == 0 {
		return fmt.Errorf("scouterkit: %s", C.GoString(&msg[0]))
	}
	return nil
}

// Assets lists the arm's store as the arm reports it now (empty without a
// store).
func Assets(arm Arm) ([]Asset, error) {
	var list [120]C.sk_g1_asset
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return nil, ErrNotInitialized
	}
	n := int(C.sk_g1_assets(C.int(arm), &list[0], C.int(len(list))))
	out := make([]Asset, 0, n)
	for i := 0; i < n && i < len(list); i++ {
		e := list[i]
		a := Asset{Offset: uint32(e.offset), Size: uint32(e.size), CRC: uint32(e.crc), Type: AssetType(e._type)}
		for k := range a.ID {
			a.ID[k] = byte(e.id[k])
		}
		out = append(out, a)
	}
	return out, nil
}

// Forget deletes the asset on both arms.
func Forget(id AssetID) error {
	var msg [256]C.char
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return ErrNotInitialized
	}
	if C.sk_g1_forget((*C.uint8_t)(unsafe.Pointer(&id[0])), &msg[0], C.size_t(len(msg))) == 0 {
		return fmt.Errorf("scouterkit: %s", C.GoString(&msg[0]))
	}
	return nil
}
