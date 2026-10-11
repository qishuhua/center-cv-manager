package centerlock

import (
	"context"
	"testing"
	"time"
)

func TestSequentialOperationCanCallLockedSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Within(ctx, "operation-test", func(ctx context.Context) error {
		release, err := AcquireContext(ctx, "operation-test")
		if err != nil {
			return err
		}
		defer release()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireContext(ctx, "operation-test")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
