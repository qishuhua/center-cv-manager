package centerlock

import (
	"context"
	"sync"
)

var locks sync.Map

// Acquire serializes state-changing operations for one center while allowing
// different centers to continue independently.
func Acquire(centerID string) func() {
	release, _ := AcquireContext(context.Background(), centerID)
	return release
}

func AcquireContext(ctx context.Context, centerID string) (func(), error) {
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
