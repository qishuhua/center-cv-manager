package modelasset

import (
	"fmt"
	"strings"
	"testing"
)

func validTestManifest() Manifest {
	return Manifest{
		SchemaVersion: manifestSchemaVersion,
		Model:         "fire-detector",
		Version:       "v1.2.0",
		Formats: []ManifestFormat{{
			ID: "nvidia-3050", Platform: "nvidia", HardwareProfile: "3050", RepositoryPath: "plan/3050/fire-detector",
			Artifacts: []ManifestArtifact{{Path: "artifact/nvidia-3050/1/model.plan", TargetPath: "plan/3050/fire-detector/1/model.plan", Size: 123, SHA256: strings.Repeat("b", 64)}},
		}},
	}
}

func TestValidateManifest(t *testing.T) {
	if err := validateManifest(validTestManifest()); err != nil {
		t.Fatal(err)
	}
	invalid := validTestManifest()
	invalid.SchemaVersion = 2
	if err := validateManifest(invalid); err == nil {
		t.Fatal("expected schema v2 to fail")
	}
	invalid = validTestManifest()
	invalid.Formats[0].Artifacts[0].TargetPath = "../escape.plan"
	if err := validateManifest(invalid); err == nil {
		t.Fatal("expected unsafe target path to fail")
	}
	invalid = validTestManifest()
	invalid.Formats[0].Artifacts[0].SHA256 = "not-a-digest"
	if err := validateManifest(invalid); err == nil {
		t.Fatal("expected invalid SHA-256 to fail")
	}
	invalid = validTestManifest()
	invalid.Formats[0].Platform = "onnx"
	if err := validateManifest(invalid); err == nil {
		t.Fatal("expected hardware_profile on a non-NVIDIA model to fail")
	}
}

func TestValidateCurrentVersionSupportsLegacyAndFlatState(t *testing.T) {
	manifest := validTestManifest()
	format := manifest.Formats[0]
	current := CurrentVersion{SchemaVersion: manifestSchemaVersion, Model: manifest.Model, Version: manifest.Version, FormatID: format.ID, Platform: format.Platform, HardwareProfile: format.HardwareProfile, RepositoryPath: format.RepositoryPath, TargetPath: format.RepositoryPath, Artifacts: format.Artifacts, ManifestDigest: strings.Repeat("c", 64)}
	if err := validateCurrentVersion(current); err != nil {
		t.Fatalf("legacy state must remain migratable: %v", err)
	}
	current.TargetPath = "plan/3050/fire-detector.plan"
	current.StoragePath = ".center-manager/repositories/fire-detector/v1/artifact/1/model.plan"
	if err := validateCurrentVersion(current); err != nil {
		t.Fatalf("flat state rejected: %v", err)
	}
	current.TargetPath = format.RepositoryPath
	if err := validateCurrentVersion(current); err == nil {
		t.Fatal("flat state accepted a nested public target")
	}
	current.TargetPath = "plan/3050/fire-detector.plan"
	current.StoragePath = "plan/3050/fire-detector/1/model.plan"
	if err := validateCurrentVersion(current); err == nil {
		t.Fatal("flat state accepted storage outside the hidden repository")
	}
	current.StoragePath = ""
	current.TargetPath = format.RepositoryPath
	current.SchemaVersion = 2
	if err := validateCurrentVersion(current); err == nil {
		t.Fatal("expected schema v2 state to fail")
	}
}

func TestNormalizeHardwareName(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
	}{
		{"NVIDIA GeForce RTX 3050", "3050"},
		{"Tesla V100-SXM2-32GB", "v100"},
	} {
		if !strings.Contains(normalizeHardwareName(test.name), normalizeHardwareName(test.profile)) {
			t.Fatalf("%q should match hardware profile %q", test.name, test.profile)
		}
	}
}

func TestModelSetPinConflict(t *testing.T) {
	digest := strings.Repeat("a", 64)
	pin := ModelSetPin{Pinned: true, ManifestDigests: map[string]string{"fire": digest, "smoke": strings.Repeat("c", 64)}}
	if !modelSetPinConflict(pin, ReleaseItem{Model: "fire", Version: "v2", ManifestDigest: strings.Repeat("b", 64)}) {
		t.Fatal("a fixed model set must reject a changed manifest")
	}
	if !modelSetPinConflict(pin, ReleaseItem{Model: "new-model", Version: "v1", ManifestDigest: strings.Repeat("d", 64)}) {
		t.Fatal("a fixed model set must reject a new model")
	}
	if modelSetPinConflict(pin, ReleaseItem{Model: "fire", Version: "v1", ManifestDigest: digest}) {
		t.Fatal("the exact manifest in a fixed set must remain idempotent")
	}
	pin.Pinned = false
	if modelSetPinConflict(pin, ReleaseItem{Model: "fire", Version: "v2", ManifestDigest: strings.Repeat("b", 64)}) {
		t.Fatal("an unfixed model set must allow switching")
	}
}

func TestPlatformCompatible(t *testing.T) {
	for _, test := range []struct {
		accelerator string
		platform    string
		want        bool
	}{
		{"nvidia", "nvidia", true},
		{"denglin", "denglin", true},
		{"nvidia", "onnx", true},
		{"denglin", "nvidia", false},
	} {
		if got := platformCompatible(test.accelerator, test.platform); got != test.want {
			t.Fatalf("platformCompatible(%q, %q) = %v", test.accelerator, test.platform, got)
		}
	}
}

func TestSelectReleaseFormat(t *testing.T) {
	item := ReleaseItem{Model: "fire", Version: "v1", Formats: []ManifestFormat{
		{ID: "onnx", Platform: "onnx"},
		{ID: "denglin", Platform: "denglin"},
		{ID: "nvidia-3050", Platform: "nvidia", HardwareProfile: "3050"},
		{ID: "nvidia-v100", Platform: "nvidia", HardwareProfile: "v100"},
	}}
	tests := []struct {
		name        string
		accelerator string
		gpus        []string
		gpuErr      error
		want        string
	}{
		{"cpu fallback", "cpu", nil, nil, "onnx"},
		{"denglin native", "denglin", nil, nil, "denglin"},
		{"nvidia exact", "nvidia", []string{"NVIDIA GeForce RTX 3050"}, nil, "nvidia-3050"},
		{"nvidia detection fallback", "nvidia", nil, fmt.Errorf("nvidia-smi failed"), "onnx"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format, err := selectReleaseFormat(item, test.accelerator, test.gpus, test.gpuErr)
			if err != nil || format.ID != test.want {
				t.Fatalf("format=%+v err=%v", format, err)
			}
		})
	}
}

func TestReleasePathGuardAllowsStableTargetButChecksParents(t *testing.T) {
	guard := releasePathGuard("/data/models", "/data/models/plan/3050/fire.plan")
	if strings.Contains(guard, "test ! -L '/data/models/plan/3050/fire.plan'") {
		t.Fatal("stable target itself may be a managed symlink")
	}
	for _, expected := range []string{"/data/models/plan/3050", "/data/models/plan", "/data/models"} {
		if !strings.Contains(guard, expected) {
			t.Fatalf("guard does not check %s: %s", expected, guard)
		}
	}
}
