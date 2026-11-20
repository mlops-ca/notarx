//go:build integration

package huggingface

import (
	"crypto/sha256"
	"encoding/hex"
)

// newSynthFile returns a synthetic LFS file of the given size with its digest computed.
func newSynthFile(size int64) *hubFile {
	sum := sha256.New()
	_ = writeSynth(sum, size)
	return &hubFile{Synth: size, SynthDigest: hex.EncodeToString(sum.Sum(nil)), LFS: true}
}

func (h *fakeHub) setRef(rev, sha string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refs[rev] = sha
}
