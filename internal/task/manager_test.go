package task_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
)

func TestNewFailsTasksInterruptedByRestart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	createdAt := time.Now().Add(-time.Hour)
	for _, value := range []*domain.Task{
		{ID: "pending", Type: "TEST", Status: task.Pending, CreatedAt: createdAt},
		{ID: "running", Type: "TEST", Status: task.Running, CreatedAt: createdAt},
		{ID: "success", Type: "TEST", Status: task.Success, Progress: 100, CreatedAt: createdAt},
	} {
		if err = st.SaveTask(value); err != nil {
			t.Fatal(err)
		}
	}

	if _, err = task.New(st); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "running"} {
		value, loadErr := st.GetTask(id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if value.Status != task.Failed || value.FinishedAt == nil || !strings.Contains(value.Error, "服务重启") {
			t.Fatalf("interrupted task was not failed: %#v", value)
		}
		logs, logErr := st.GetLogs(id, 10)
		if logErr != nil || len(logs) != 1 || !strings.Contains(logs[0], "服务重启") {
			t.Fatalf("interrupted task log = %#v, %v", logs, logErr)
		}
	}
	succeeded, err := st.GetTask("success")
	if err != nil || succeeded.Status != task.Success || succeeded.FinishedAt != nil {
		t.Fatalf("completed task was changed: %#v, %v", succeeded, err)
	}

	// Recovery must be idempotent across subsequent restarts.
	if _, err = task.New(st); err != nil {
		t.Fatal(err)
	}
	logs, err := st.GetLogs("running", 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("recovery appended duplicate logs: %#v, %v", logs, err)
	}
}

func TestCancelAndDeleteTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := task.New(st)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	v := m.Submit("TEST", nil, func(ctx context.Context, h *task.Handle) error {
		h.Log("before cancellation")
		close(started)
		<-ctx.Done()
		return nil // Even an executor returning nil must finish as CANCELLED.
	})
	<-started
	if current, ok := m.Get(v.ID); !ok || current.LogCount != 1 {
		t.Fatalf("in-memory log count = %#v, want 1", current)
	}
	if err := m.Delete(v.ID); err == nil {
		t.Fatal("deleted a running task")
	}
	if err := m.Cancel(v.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := m.Get(v.ID)
		if current.Status == task.Cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not stop")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(v.ID); ok {
		t.Fatal("task record retained in memory")
	}
	logs, err := st.GetLogs(v.ID, 100)
	if err != nil || len(logs) != 0 {
		t.Fatalf("database logs retained after task deletion: %v %v", logs, err)
	}
	if _, err := st.GetTask(v.ID); err != store.ErrNotFound {
		t.Fatalf("persisted task record retained: %v", err)
	}
	if err := m.Cancel(v.ID); err == nil {
		t.Fatal("accepted stop for deleted task")
	}
	if _, _, _, err := m.Subscribe(v.ID); err == nil {
		t.Fatal("subscribed to deleted task")
	}
}

func TestLogsReturnsEmptyArrayInsteadOfNil(t *testing.T) {
	m, err := task.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if logs := m.Logs("missing", 100); logs == nil {
		t.Fatal("Logs returned nil; API clients require an empty JSON array")
	}
}

func TestCancelAllIncludesTasksOutsideListLimit(t *testing.T) {
	m, err := task.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	const count = 105
	finished := make(chan struct{}, count)
	started := make(chan struct{}, count)
	for i := 0; i < count; i++ {
		m.Submit("TEST", nil, func(ctx context.Context, h *task.Handle) error {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return ctx.Err()
		})
	}
	for i := 0; i < count; i++ {
		<-started
	}
	if got := m.CancelAll(); got != count {
		t.Fatalf("stopped %d tasks, want %d", got, count)
	}
	for i := 0; i < count; i++ {
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("task was not cancelled")
		}
	}
}
