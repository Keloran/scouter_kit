// Package lc3 compiles liblc3 1.1.3 (Google, Apache-2.0, vendored in
// ../../../third_party/liblc3 with its licence) into the program: the LC3
// decoder of the library's microphone and the encoder of the emulated arms.
// cgo compiles only the files in a package's directory, so these wrappers
// include the sources.
package lc3

// #cgo CFLAGS: -DLC3_PLUS=0 -DLC3_PLUS_HR=0 -I${SRCDIR}/../../../third_party/liblc3/include
// #cgo LDFLAGS: -lm
import "C"
