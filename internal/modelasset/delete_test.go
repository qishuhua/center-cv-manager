package modelasset

import (
	"strings"
	"testing"
)

func TestModelFolderPrefixIsolated(t *testing.T) {
	for _, root := range []string{"center-models", "center-models/", "nested/models/"} {
		prefix, err := modelFolderPrefix(root, "fire")
		if err != nil {
			t.Fatal(err)
		}
		base := strings.TrimRight(root, "/") + "/"
		if !strings.HasPrefix(base+"fire/v1/manifest.json", prefix) {
			t.Fatalf("model object excluded: %s", prefix)
		}
		for _, key := range []string{base + "fire-v2/v1/manifest.json", base + "firefly/v1/a", base + ".uploads/a"} {
			if strings.HasPrefix(key, prefix) {
				t.Fatalf("unrelated object included: %s", key)
			}
		}
	}
}

func TestDeleteModelRejectsUnsafeNamesBeforeOSS(t *testing.T) {
	s := &Service{}
	for _, name := range []string{"", ".", "..", "../fire", "fire/other", "fire\\other", "/fire", ".uploads", strings.Repeat("a", 129)} {
		if err := s.DeleteModel(name); err == nil {
			t.Errorf("accepted unsafe model name %q", name)
		}
	}
}
