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

func TestEmptyFoldersFromObjects(t *testing.T) {
	objects := []Object{
		{Name: "fire/v1/manifest.json", Size: 10},
		{Name: "fire/v1/weights.bin", Size: 100},
		{Name: "smoke/leftover.bin", Size: 5},   // 无 manifest → 空文件夹
		{Name: "smoke/deep/path/a", Size: 7},    // 深层残留同样计入
		{Name: "mixed/v2/manifest.json", Size: 10},
		{Name: "mixed/stray.bin", Size: 3},      // 有可用版本的文件夹不算空
		{Name: ".uploads/staging/a", Size: 1},   // 隐藏目录忽略
		{Name: "orphan.bin", Size: 2},           // 顶层散文件不属于任何文件夹
	}
	folders := emptyFoldersFromObjects(objects)
	if len(folders) != 1 || folders[0].Model != "smoke" || folders[0].Files != 2 || folders[0].Size != 12 {
		t.Fatalf("folders = %#v", folders)
	}
}
