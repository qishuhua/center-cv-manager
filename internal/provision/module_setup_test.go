package provision

import "testing"

func TestModuleSetupPaths(t *testing.T) {
	valid := ModuleSetup{Root: "/home/jt/deploy", ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaRoot: "/data/media-resources"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "/", "relative", "~/deploy", "/home/../data", "/data/", "/data\nother"} {
		for field := 0; field < 4; field++ {
			in := valid
			fields := []*string{&in.Root, &in.ModelsRoot, &in.LogsRoot, &in.MediaRoot}
			*fields[field] = bad
			if in.Validate() == nil {
				t.Fatalf("accepted field %d path %q", field, bad)
			}
		}
	}
}
