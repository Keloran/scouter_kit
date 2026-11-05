//go:build !sherpa

package speech

import "errors"

// Streaming reports whether the build has a streaming recogniser.
const Streaming = false

// OpenLive needs a build with -tags sherpa (README).
func OpenLive(string, int) (Live, error) {
	return nil, errors.New("speech: built without the streaming recogniser; build with -tags sherpa (see the README)")
}
