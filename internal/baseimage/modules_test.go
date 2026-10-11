package baseimage

import "testing"

func TestImageApplicationRetriesLoadedButUnappliedImage(t *testing.T) {
	applicable := map[string]bool{"rabbitmq:latest": true}
	desired := map[string]string{"rabbitmq:latest": "new-id", "worker:1": "worker-new-id"}
	if !imageNeedsApply("rabbitmq:latest", "old-id", applicable, desired) {
		t.Fatal("already loaded image must still be applied to an old container on retry")
	}
	if imageNeedsApply("rabbitmq:latest", "new-id", applicable, desired) {
		t.Fatal("unchanged container would be recreated")
	}
	if imageNeedsApply("worker:1", "worker-old-id", applicable, desired) {
		t.Fatal("unrelated source image would be applied")
	}
}
