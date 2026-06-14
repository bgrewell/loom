// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"sync"
	"testing"
)

// TestMemoryCloseConcurrentSend ensures Close never panics a concurrent Send
// (the old close-the-channel implementation paniced with "send on closed
// channel"). Run with -race.
func TestMemoryCloseConcurrentSend(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := NewMemory(4)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = m.Send([]byte("x")) // must not panic even after Close
			}
		}()
		go func() {
			defer wg.Done()
			_ = m.Close()
		}()
		wg.Wait()
	}
}
