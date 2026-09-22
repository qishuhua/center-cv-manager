package centerlock

import (
	"context"
	"testing"
	"time"
)

func TestWaitingTaskCanCancel(t *testing.T) {
	release := Acquire("test")
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := AcquireContext(ctx, "test"); result <- err }()
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled task still waiting for center")
	}
}
