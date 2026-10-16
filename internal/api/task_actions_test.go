package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func TestDeleteTaskRemovesRecordAndLogs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tasks, err := task.New(st)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, tasks: tasks}
	if err := st.SaveTask(&domain.Task{ID: "legacy-model", Type: "MODEL_SYNC", Status: task.Failed, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("legacy-model", "old log"); err != nil {
		t.Fatal(err)
	}
	if persisted, err := st.GetTask("legacy-model"); err != nil || persisted.LogCount != 1 {
		t.Fatalf("log count before deletion = %#v, %v", persisted, err)
	}
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Params = gin.Params{{Key: "id", Value: "legacy-model"}}
	s.deleteTask(c)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if _, err := st.GetTask("legacy-model"); err != store.ErrNotFound {
		t.Fatalf("task record still exists after deletion: %v", err)
	}
	logs, err := st.GetLogs("legacy-model", 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("task logs retained after record deletion: %v %v", logs, err)
	}
}
