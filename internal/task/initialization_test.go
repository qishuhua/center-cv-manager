package task

import (
	"context"
	"testing"
	"time"
)

func TestHasActiveTracksInitializationUntilExecutorFinishes(t *testing.T) {
	m, _ := New(nil)
	release := make(chan struct{})
	task := m.Submit("INITIALIZE_CENTER", map[string]string{"center_id": "one"}, func(context.Context, *Handle) error { <-release; return nil })
	if !m.HasActive("INITIALIZE_CENTER", "center_id", "one") {
		t.Fatal("pending/running initialization not found")
	}
	if m.HasActive("INITIALIZE_CENTER", "center_id", "two") {
		t.Fatal("blocked unrelated center")
	}
	close(release)
	deadline := time.After(time.Second)
	for m.HasActive("INITIALIZE_CENTER", "center_id", "one") {
		select {
		case <-deadline:
			t.Fatal("finished initialization stayed active")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got, _ := m.Get(task.ID); got.Status != Success {
		t.Fatal(got.Status)
	}
}
