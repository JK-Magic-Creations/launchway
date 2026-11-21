package runner

import (
	"fmt"
	"testing"
	"time"
)

func TestLogBufferBackpressureIsBoundedAndLosesNothing(t *testing.T) {
	b := newLogBuffer()
	done := make(chan struct{})
	go func() {
		for i := 0; i < logBufferMax+100; i++ {
			b.add("stdout", fmt.Sprint(i))
		}
		close(done)
	}()
	select { // a drain frees the writer well before the longest wait
	case <-time.After(300 * time.Millisecond):
	case <-done:
		t.Fatal("the writer was not held at the bound")
	}
	got := 0
	for got < logBufferMax+100 {
		got += len(b.take(2000))
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the writer stayed held after a drain")
	}
}

func TestLogBufferNeverBlocksForever(t *testing.T) {
	b := newLogBuffer()
	start := time.Now()
	for i := 0; i < logBufferMax+1; i++ {
		b.add("stdout", "x") // nobody drains
	}
	if d := time.Since(start); d > logBufferWait+2*time.Second {
		t.Fatalf("held for %s", d)
	}
	if n := len(b.take(logBufferMax + 10)); n != logBufferMax+1 {
		t.Fatalf("%d lines", n)
	}
}
