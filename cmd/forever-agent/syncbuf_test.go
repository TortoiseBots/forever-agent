package main

// A stderr the test can read while the CLI still writes to it: the login tests run the command in a goroutine and
// poll its output, on every platform, so the helper lives outside the Unix-only signal tests.

import (
	"bytes"
	"sync"
)

// syncBuffer is a stderr the test can read while the CLI still writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
