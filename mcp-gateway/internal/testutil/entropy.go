package testutil

import (
	"io"
	"sync"
)

type FakeEntropy struct {
	mu     sync.Mutex
	bytes  []byte
	offset int
}

func NewFakeEntropy(bytes []byte) *FakeEntropy {
	return &FakeEntropy{bytes: append([]byte(nil), bytes...)}
}

func (entropy *FakeEntropy) Read(destination []byte) (int, error) {
	entropy.mu.Lock()
	defer entropy.mu.Unlock()

	if entropy.offset == len(entropy.bytes) {
		return 0, io.EOF
	}
	count := copy(destination, entropy.bytes[entropy.offset:])
	entropy.offset += count
	return count, nil
}

func (entropy *FakeEntropy) Remaining() int {
	entropy.mu.Lock()
	defer entropy.mu.Unlock()
	return len(entropy.bytes) - entropy.offset
}
