package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"center-cv-manager/internal/domain"
	"github.com/google/uuid"
)

const (
	Pending   = "PENDING"
	Running   = "RUNNING"
	Success   = "SUCCESS"
	Failed    = "FAILED"
	Cancelled = "CANCELLED"
)

type Store interface {
	SaveTask(*domain.Task) error
	AppendLog(string, string) error
	FailInterruptedTasks(time.Time, string) (int, error)
	ListTasks(int) ([]*domain.Task, error)
	GetTask(string) (*domain.Task, error)
	GetLogs(string, int) ([]string, error)
	DeleteTask(string) error
}

type Event struct {
	Name string `json:"name"`
	Data any    `json:"data"`
}
type Executor func(context.Context, *Handle) error
type running struct {
	task        *domain.Task
	cancel      context.CancelFunc
	mu          sync.Mutex
	logs        []string
	history     []Event
	subscribers map[chan Event]struct{}
	done        chan struct{}
}
type Manager struct {
	mu     sync.RWMutex
	active map[string]*running
	store  Store
}
type Handle struct {
	manager *Manager
	running *running
}

const interruptedMessage = "管理服务重启，任务执行已中断"

func New(store Store) (*Manager, error) {
	m := &Manager{active: map[string]*running{}, store: store}
	if store == nil {
		return m, nil
	}
	if _, err := store.FailInterruptedTasks(time.Now(), interruptedMessage); err != nil {
		return nil, fmt.Errorf("恢复中断任务: %w", err)
	}
	return m, nil
}

func (m *Manager) Submit(kind string, params map[string]string, fn Executor) *domain.Task {
	now := time.Now()
	t := &domain.Task{ID: "task-" + uuid.NewString(), Type: kind, Status: Pending, Params: params, CreatedAt: now}
	r := &running{task: t, subscribers: map[chan Event]struct{}{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	m.mu.Lock()
	m.active[t.ID] = r
	m.mu.Unlock()
	m.persist(t)
	copy := *t
	go func() {
		defer close(r.done)
		defer cancel()
		h := &Handle{manager: m, running: r}
		started := time.Now()
		m.update(r, func(t *domain.Task) { t.Status = Running; t.StartedAt = &started })
		h.emit("status", map[string]any{"status": Running})
		err := ctx.Err()
		if err == nil {
			err = fn(ctx, h)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			finished := time.Now()
			status := Failed
			if ctx.Err() != nil {
				status = Cancelled
			}
			m.update(r, func(t *domain.Task) { t.Status = status; t.Error = err.Error(); t.FinishedAt = &finished })
			if status == Cancelled {
				h.Log("任务已停止")
			} else {
				h.Log("任务失败: " + err.Error())
			}
			h.emit("status", map[string]any{"status": status, "error": err.Error()})
			return
		}
		finished := time.Now()
		completionMessage := ""
		completionStatus := Success
		m.update(r, func(t *domain.Task) {
			if ctx.Err() != nil {
				completionStatus = Cancelled
				t.Error = ctx.Err().Error()
				t.Message = "任务已停止"
			} else {
				t.Progress = 100
			}
			t.Status = completionStatus
			t.FinishedAt = &finished
			completionMessage = t.Message
		})
		h.emit("status", map[string]any{"status": completionStatus, "message": completionMessage})
	}()
	return &copy
}

func (m *Manager) update(r *running, fn func(*domain.Task)) {
	m.mu.Lock()
	fn(r.task)
	copy := *r.task
	m.mu.Unlock()
	m.persist(&copy)
}
func (m *Manager) persist(t *domain.Task) {
	if m.store != nil {
		_ = m.store.SaveTask(t)
	}
}
func (h *Handle) Message(v string) {
	h.manager.update(h.running, func(t *domain.Task) { t.Message = v })
	h.emit("progress", map[string]any{"message": v})
}
func (h *Handle) Progress(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	h.manager.update(h.running, func(t *domain.Task) { t.Progress = v })
	h.emit("progress", map[string]any{"progress": v})
}
func (h *Handle) Log(v string) {
	line := time.Now().Format("15:04:05") + " " + v
	h.manager.mu.Lock()
	h.running.task.LogCount++
	h.manager.mu.Unlock()
	h.running.mu.Lock()
	h.running.logs = append(h.running.logs, line)
	h.running.mu.Unlock()
	if h.manager.store != nil {
		_ = h.manager.store.AppendLog(h.running.task.ID, line)
	}
	h.emit("log", map[string]any{"message": line})
}
func (h *Handle) Logf(f string, a ...any) { h.Log(fmt.Sprintf(f, a...)) }
func (h *Handle) emit(name string, data any) {
	e := Event{Name: name, Data: data}
	h.running.mu.Lock()
	h.running.history = append(h.running.history, e)
	if len(h.running.history) > 1000 {
		h.running.history = h.running.history[len(h.running.history)-1000:]
	}
	for ch := range h.running.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
	h.running.mu.Unlock()
}

func (m *Manager) Get(id string) (*domain.Task, bool) {
	m.mu.RLock()
	r, ok := m.active[id]
	if ok {
		copy := *r.task
		m.mu.RUnlock()
		return &copy, true
	}
	m.mu.RUnlock()
	if m.store != nil {
		t, e := m.store.GetTask(id)
		return t, e == nil
	}
	return nil, false
}
func (m *Manager) List(limit int) []*domain.Task {
	if m.store == nil {
		return []*domain.Task{}
	}
	v, _ := m.store.ListTasks(limit)
	return v
}
func (m *Manager) Logs(id string, limit int) []string {
	m.mu.RLock()
	r, ok := m.active[id]
	m.mu.RUnlock()
	if ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		start := 0
		if limit > 0 && len(r.logs) > limit {
			start = len(r.logs) - limit
		}
		return append([]string{}, r.logs[start:]...)
	}
	if m.store != nil {
		v, _ := m.store.GetLogs(id, limit)
		if v == nil {
			return []string{}
		}
		return v
	}
	return []string{}
}
func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.active[id]
	if !ok || (r.task.Status != Pending && r.task.Status != Running) {
		return fmt.Errorf("任务不存在或已结束")
	}
	r.cancel()
	return nil
}

// CancelAll uses the complete in-memory set, independent of list pagination.
func (m *Manager) CancelAll() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, r := range m.active {
		if r.task.Status == Pending || r.task.Status == Running {
			r.cancel()
			count++
		}
	}
	return count
}

func (m *Manager) Delete(id string) error {
	t, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if t.Status == Pending || t.Status == Running {
		return fmt.Errorf("请先停止任务再删除记录")
	}
	m.mu.RLock()
	r := m.active[id]
	m.mu.RUnlock()
	if r != nil {
		<-r.done // Wait for the final log and status event to be written.
	}
	if m.store != nil {
		if err := m.store.DeleteTask(id); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Subscribe(id string) (chan Event, []Event, func(), error) {
	m.mu.RLock()
	r, ok := m.active[id]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, nil, fmt.Errorf("任务已结束，请读取任务日志")
	}
	ch := make(chan Event, 128)
	r.mu.Lock()
	r.subscribers[ch] = struct{}{}
	history := append([]Event(nil), r.history...)
	r.mu.Unlock()
	cancel := func() { r.mu.Lock(); delete(r.subscribers, ch); r.mu.Unlock() }
	return ch, history, cancel, nil
}
