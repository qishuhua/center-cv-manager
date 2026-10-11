package centerlock

import (
	"context"
	"sync"
)

var locks sync.Map

type operationKey struct{}

// Within holds the lock across a multi-step release. Only its sequential
// internal steps receive this context; it must not be shared with parallel work.
func Within(ctx context.Context, centerID string, run func(context.Context) error) error {
	release, err := AcquireContext(ctx, centerID)
	if err != nil {
		return err
	}
	defer release()
	return run(context.WithValue(ctx, operationKey{}, centerID))
}

// Acquire serializes state-changing operations for one center while allowing
// different centers to continue independently.
func Acquire(centerID string) func() {
	release, _ := AcquireContext(context.Background(), centerID)
	return release
}

func AcquireContext(ctx context.Context, centerID string) (func(), error) {
	if ctx.Value(operationKey{}) == centerID {
		return func() {}, ctx.Err()
	}
	value, _ := locks.LoadOrStore(centerID, make(chan struct{}, 1))
	semaphore := value.(chan struct{})
	select {
	case semaphore <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-semaphore
			return nil, err
		}
		return func() { <-semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
