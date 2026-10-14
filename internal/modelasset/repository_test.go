package modelasset

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validRepositoryManifest() Manifest {
	return validTestManifest()
}

func TestRepositoryManifestValidation(t *testing.T) {
	if err := validateManifest(validRepositoryManifest()); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Manifest){
		"old schema":           func(m *Manifest) { m.SchemaVersion = 2 },
		"empty formats":        func(m *Manifest) { m.Formats = nil },
		"outside artifact":     func(m *Manifest) { m.Formats[0].Artifacts[0].Path = "../secret" },
		"outside target":       func(m *Manifest) { m.Formats[0].Artifacts[0].TargetPath = "onnx/other/model.plan" },
		"reserved repository":  func(m *Manifest) { m.Formats[0].RepositoryPath = ".center-manager" },
		"traversal repository": func(m *Manifest) { m.Formats[0].RepositoryPath = "../models" },
		"duplicate artifact": func(m *Manifest) {
			m.Formats[0].Artifacts = append(m.Formats[0].Artifacts, m.Formats[0].Artifacts[0])
		},
		"duplicate format": func(m *Manifest) { m.Formats = append(m.Formats, m.Formats[0]) },
		"negative size":    func(m *Manifest) { m.Formats[0].Artifacts[0].Size = -1 },
		"invalid digest":   func(m *Manifest) { m.Formats[0].Artifacts[0].SHA256 = "bad" },
		"missing gpu":      func(m *Manifest) { m.Formats[0].HardwareProfile = "" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			m := validRepositoryManifest()
			change(&m)
			if validateManifest(m) == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestParseRepositoryVersion(t *testing.T) {
	m := validRepositoryManifest()
	data, _ := json.Marshal(m)
	key := "center-models/nvidia-3050/2026.09.06-021211/manifest.json"
	objects := map[string]Object{}
	for _, format := range m.Formats {
		for _, artifact := range format.Artifacts {
			objects[strings.TrimSuffix(key, "manifest.json")+artifact.Path] = Object{Size: artifact.Size}
		}
	}
	v, err := parseVersion(data, key, objects)
	if err != nil {
		t.Fatal(err)
	}
	if v.SchemaVersion != manifestSchemaVersion || len(v.Formats) != 1 || artifactBytes(v.Formats[0].Artifacts) != 123 || len(v.ManifestDigest) != 64 {
		t.Fatalf("unexpected version: %+v", v)
	}
	missingKey := strings.TrimSuffix(key, "manifest.json") + m.Formats[0].Artifacts[0].Path
	objects[missingKey] = Object{Size: 999}
	if _, err := parseVersion(data, key, objects); err == nil {
		t.Fatal("accepted mismatched size")
	}
	delete(objects, missingKey)
	if _, err := parseVersion(data, key, objects); err == nil {
		t.Fatal("accepted missing config file")
	}
	old := m
	old.SchemaVersion = 2
	data, _ = json.Marshal(old)
	if _, err := parseVersion(data, key, objects); err == nil {
		t.Fatal("accepted schema v2 manifest")
	}
}

func TestReleaseTargetOverlap(t *testing.T) {
	m := validRepositoryManifest()
	format := m.Formats[0]
	repository := ReleaseItem{SchemaVersion: manifestSchemaVersion, Model: m.Model, Version: m.Version, FormatID: format.ID, Platform: format.Platform, HardwareProfile: format.HardwareProfile, RepositoryPath: format.RepositoryPath, Artifacts: format.Artifacts}
	if validateReleaseTargets([]ReleaseItem{repository}) != nil {
		t.Fatal("valid repository rejected")
	}
	old := repository
	old.SchemaVersion = 2
	if validateReleaseTargets([]ReleaseItem{old}) == nil {
		t.Fatal("old schema target accepted")
	}
	if validateReleaseTargets([]ReleaseItem{repository, repository}) == nil {
		t.Fatal("duplicate repository accepted")
	}
	overlap := repository
	overlap.Model = "smoke-detector"
	overlap.RepositoryPath += "/nested"
	if validateReleaseTargets([]ReleaseItem{repository, overlap}) == nil {
		t.Fatal("overlapping target accepted")
	}
}

func TestRepositoryStageArtifactPathUsesRepositoryRelativeTarget(t *testing.T) {
	got, err := repositoryStageArtifactPath(
		"/data/models/.center-manager/repositories/cargo/v1/digest/artifact",
		"denglin/cargo",
		"denglin/cargo/cargo.engine",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "/data/models/.center-manager/repositories/cargo/v1/digest/artifact/cargo.engine"
	if got != want {
		t.Fatalf("repositoryStageArtifactPath() = %q, want %q", got, want)
	}
	if _, err = repositoryStageArtifactPath("/stage", "denglin/cargo", "denglin/other/cargo.engine"); err == nil {
		t.Fatal("accepted artifact target outside the selected repository")
	}
}

func TestRuntimeAliasUsesCanonicalInferencePath(t *testing.T) {
	tests := []struct {
		name       string
		item       ReleaseItem
		wantAlias  string
		wantTarget string
	}{
		{
			name:       "onnx",
			item:       ReleaseItem{Model: "fire", Platform: "onnx", RepositoryPath: "onnx/fire", Artifacts: []ManifestArtifact{{TargetPath: "onnx/fire/fire_detector.onnx"}}},
			wantAlias:  "onnx/fire.onnx",
			wantTarget: "fire/fire_detector.onnx",
		},
		{
			name:       "denglin",
			item:       ReleaseItem{Model: "falldown", Platform: "denglin", RepositoryPath: "denglin/falldown", Artifacts: []ManifestArtifact{{TargetPath: "denglin/falldown/yolo.engine"}}},
			wantAlias:  "denglin/falldown.engine",
			wantTarget: "falldown/yolo.engine",
		},
		{
			name:       "nvidia",
			item:       ReleaseItem{Model: "crossover", Platform: "nvidia", HardwareProfile: "3050", RepositoryPath: "plan/3050/crossover", Artifacts: []ManifestArtifact{{TargetPath: "plan/3050/crossover/model.plan"}}},
			wantAlias:  "plan/crossover.plan",
			wantTarget: "3050/crossover/model.plan",
		},
		{
			name:       "nvidia V100",
			item:       ReleaseItem{Model: "violence", Platform: "nvidia", HardwareProfile: "V100", RepositoryPath: "plan/V100/violence", Artifacts: []ManifestArtifact{{TargetPath: "plan/V100/violence/violence.plan"}}},
			wantAlias:  "plan/violence.plan",
			wantTarget: "V100/violence/violence.plan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias, target, _, err := runtimeAlias(tt.item)
			if err != nil {
				t.Fatal(err)
			}
			if alias != tt.wantAlias || target != tt.wantTarget {
				t.Fatalf("runtimeAlias() = (%q, %q), want (%q, %q)", alias, target, tt.wantAlias, tt.wantTarget)
			}
		})
	}
}

func TestRuntimeAliasRejectsAmbiguousArtifacts(t *testing.T) {
	item := ReleaseItem{Model: "fire", Platform: "denglin", Artifacts: []ManifestArtifact{{TargetPath: "denglin/fire/a.engine"}, {TargetPath: "denglin/fire/b.engine"}}}
	if _, _, _, err := runtimeAlias(item); err == nil {
		t.Fatal("runtime alias accepted multiple model files")
	}
}

func TestNVIDIARuntimeAliasRejectsWrongHardwareArtifact(t *testing.T) {
	item := ReleaseItem{Model: "fire", Platform: "nvidia", HardwareProfile: "V100", Artifacts: []ManifestArtifact{{TargetPath: "plan/3050/fire/model.plan"}}}
	if _, _, _, err := runtimeAlias(item); err == nil {
		t.Fatal("flattening the inference path must not bypass hardware profile validation")
	}
}

func TestRepairProfileQualifiedNVIDIACurrentVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Linux shell and symlinks")
	}
	root := t.TempDir()
	content := []byte("existing V100 engine")
	storage := ".center-manager/repositories/fire/v1/artifact/fire.plan"
	stored := filepath.Join(root, storage)
	if err := os.MkdirAll(filepath.Dir(stored), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, content, 0644); err != nil {
		t.Fatal(err)
	}
	item := ReleaseItem{Model: "fire", Version: "v1", Platform: "nvidia", HardwareProfile: "V100", RepositoryPath: "plan/V100/fire", Artifacts: []ManifestArtifact{{TargetPath: "plan/V100/fire/fire.plan", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}}}
	current := CurrentVersion{Model: item.Model, Version: item.Version, Platform: item.Platform, HardwareProfile: item.HardwareProfile, RepositoryPath: item.RepositoryPath, Artifacts: item.Artifacts, TargetPath: "plan/V100/fire.plan", StoragePath: storage, ActivatedAt: "2026-10-14T00:00:00Z"}
	oldAlias := filepath.Join(root, current.TargetPath)
	if err := os.MkdirAll(filepath.Dir(oldAlias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relativeSymlinkTarget(filepath.Dir(oldAlias), stored), oldAlias); err != nil {
		t.Fatal(err)
	}
	state := releaseStatePath(root, item.Model)
	if err := os.MkdirAll(filepath.Dir(state), 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(current)
	if err := os.WriteFile(state, data, 0644); err != nil {
		t.Fatal(err)
	}
	files := remoteFiles{root: root, client: permissionSSH(t, "filesystem")}
	for i := 0; i < 2; i++ {
		updated, err := repairStoredCurrentVersion(context.Background(), files, current, item, quietReporter{})
		if err != nil {
			t.Fatal(err)
		}
		if updated.TargetPath != "plan/fire.plan" || updated.StoragePath != storage || updated.Version != current.Version || updated.ActivatedAt != current.ActivatedAt {
			t.Fatalf("unexpected migration: %+v", updated)
		}
		actual, err := os.ReadFile(filepath.Join(root, "plan/fire.plan"))
		if err != nil || string(actual) != string(content) {
			t.Fatalf("inference cannot read engine: %q %v", actual, err)
		}
		data, err = os.ReadFile(state)
		if err != nil {
			t.Fatal(err)
		}
		var saved CurrentVersion
		if err = json.Unmarshal(data, &saved); err != nil || saved.TargetPath != updated.TargetPath {
			t.Fatalf("state was not migrated: %s %v", data, err)
		}
		current = updated
	}
}

func TestRelativeSymlinkTargetStaysInsideBindMount(t *testing.T) {
	got := relativeSymlinkTarget(
		"/home/ops/.local/share/center-manager/data/models/denglin",
		"/home/ops/.local/share/center-manager/data/models/.center-manager/repositories/fire/v1/artifact",
	)
	want := "../.center-manager/repositories/fire/v1/artifact"
	if got != want {
		t.Fatalf("relativeSymlinkTarget() = %q, want %q", got, want)
	}
	if rootTarget := relativeSymlinkTarget("/", "/data/models"); rootTarget != "data/models" {
		t.Fatalf("root-relative target = %q", rootTarget)
	}
}

// These tests use only t.TempDir(), never a real center or /data/models.
func TestFlatRuntimeActivationAndRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux activation script tested in the build container")
	}
	for _, initial := range []string{"absent", "directory", "symlink"} {
		for _, failState := range []bool{false, true} {
			name := initial
			if failState {
				name += "-rollback"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				target, stage, state, backup := filepath.Join(root, "denglin", "fire.engine"), filepath.Join(root, ".center-manager", "repositories", "fire", "stage"), filepath.Join(root, "state.json"), filepath.Join(root, "backup")
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(stage, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stage, "fire.engine"), []byte("new"), 0644); err != nil {
					t.Fatal(err)
				}
				if initial == "directory" {
					if err := os.Mkdir(target, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(target, "old"), []byte("old"), 0644); err != nil {
						t.Fatal(err)
					}
				} else if initial == "symlink" {
					old := filepath.Join(root, "old-version")
					if err := os.Mkdir(old, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(old, "old"), []byte("old"), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(old, target); err != nil {
						t.Fatal(err)
					}
				}
				if failState {
					if err := os.Mkdir(state, 0755); err != nil {
						t.Fatal(err)
					}
				}
				stored := filepath.Join(stage, "fire.engine")
				output, err := exec.Command("sh", "-c", runtimeActivationCommand(target, stored, state, backup, `{"version":"new"}`)).CombinedOutput()
				if failState {
					if err == nil {
						t.Fatal("expected publication failure")
					}
					if initial == "absent" {
						if _, err := os.Lstat(target); !os.IsNotExist(err) {
							t.Fatal("failed release left active target")
						}
					} else if _, err := os.Stat(filepath.Join(target, "old")); err != nil {
						t.Fatalf("old version not restored: %v\n%s", err, output)
					}
				} else {
					if err != nil {
						t.Fatalf("activation: %v\n%s", err, output)
					}
					wantLink := relativeSymlinkTarget(filepath.Dir(target), stored)
					if link, err := os.Readlink(target); err != nil || link != wantLink {
						t.Fatalf("target=%s, want %s, err=%v", link, wantLink, err)
					}
					if data, err := os.ReadFile(state); err != nil || string(data) != `{"version":"new"}` {
						t.Fatalf("bad state: %s %v", data, err)
					}
					if initial == "directory" {
						if _, err := os.Stat(filepath.Join(backup, "old")); err != nil {
							t.Fatal("legacy backup missing")
						}
					}
				}
			})
		}
	}
}
