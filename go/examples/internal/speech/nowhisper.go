//go:build !whisper

package speech

import "errors"

// Available reports whether the build has a recogniser.
const Available = false

// Open needs a build with -tags whisper (README).
func Open(Model) (Recognizer, error) {
	return nil, errors.New("speech: built without the recogniser; build with -tags whisper (see the README)")
}
