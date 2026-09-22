package modelasset

import (
	"bytes"
	"encoding/json"
	"testing"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func validModelUpload() ModelUpload {
	return ModelUpload{
		UploadID: "d66a7c86-9e64-4b49-bafd-3fd3f6fdac16",
		Model:    "fire",
		Version:  "2026.09.06-001",
		Formats: []ModelUploadFormat{
			{ID: "onnx", Platform: "onnx", Files: []ModelUploadFile{{Path: "fire_detector.onnx", Size: 100}}},
			{ID: "nvidia-3050", Platform: "nvidia", HardwareProfile: "3050", Files: []ModelUploadFile{{Path: "fire_detector_3050.plan", Size: 200}}},
		},
	}
}

func TestModelUploadReaderReportsExactContentLength(t *testing.T) {
	const size = int64(20339777)
	reader := modelUploadReader(bytes.NewReader(nil), size)
	length, err := aliyunoss.GetReaderLen(reader)
	if err != nil {
		t.Fatal(err)
	}
	if length != size {
		t.Fatalf("OSS content length = %d, want %d", length, size)
	}
}

func TestNormalizeModelUploadBuildsLogicalVersion(t *testing.T) {
	manifest, normalized, err := normalizeModelUpload(validModelUpload())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.Model != "fire" || len(manifest.Formats) != 2 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if manifest.Formats[0].RepositoryPath != "onnx/fire" || manifest.Formats[1].RepositoryPath != "plan/3050/fire" {
		t.Fatalf("unexpected targets: %+v", manifest.Formats)
	}
	if normalized.Formats[0].Files[0].Path != "fire_detector.onnx" || manifest.Formats[0].Artifacts[0].Path != "artifact/onnx/fire_detector.onnx" || manifest.Formats[0].Artifacts[0].TargetPath != "onnx/fire/fire_detector.onnx" {
		t.Fatal("files were not normalized deterministically")
	}
	if err = validateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	item := ReleaseItem{SchemaVersion: manifestSchemaVersion, Model: manifest.Model, Version: manifest.Version, Formats: manifest.Formats}
	if err = validateReleaseTargets([]ReleaseItem{item}); err != nil {
		t.Fatalf("logical selection rejected: %v", err)
	}
	item.Platform, item.HardwareProfile = manifest.Formats[1].Platform, manifest.Formats[1].HardwareProfile
	item.RepositoryPath, item.Artifacts, item.Formats = manifest.Formats[1].RepositoryPath, manifest.Formats[1].Artifacts, nil
	if err = validateReleaseTargets([]ReleaseItem{item}); err != nil {
		t.Fatalf("resolved variant rejected: %v", err)
	}
}

func TestNormalizeModelUploadRejectsInvalidFormats(t *testing.T) {
	tests := map[string]func(*ModelUpload){
		"missing model": func(input *ModelUpload) { input.Formats[0].Files = nil },
		"extra file": func(input *ModelUpload) {
			input.Formats[0].Files = append(input.Formats[0].Files, ModelUploadFile{Path: "extra.txt", Size: 1})
		},
		"wrong extension": func(input *ModelUpload) { input.Formats[1].Files[0].Path = "fire_detector.engine" },
		"nested path":     func(input *ModelUpload) { input.Formats[0].Files[0].Path = "1/fire_detector.onnx" },
		"missing gpu":     func(input *ModelUpload) { input.Formats[1].HardwareProfile = "" },
		"duplicate":       func(input *ModelUpload) { input.Formats = append(input.Formats, input.Formats[0]) },
		"traversal":       func(input *ModelUpload) { input.Formats[0].Files[0].Path = "../fire_detector.onnx" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			input := validModelUpload()
			change(&input)
			if _, _, err := normalizeModelUpload(input); err == nil {
				t.Fatal("accepted invalid upload")
			}
		})
	}
}

func TestParseLogicalVersion(t *testing.T) {
	manifest, _, err := normalizeModelUpload(validModelUpload())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	key := "center-models/fire/2026.09.06-001/manifest.json"
	objects := map[string]Object{}
	for _, format := range manifest.Formats {
		for _, artifact := range format.Artifacts {
			objects["center-models/fire/2026.09.06-001/"+artifact.Path] = Object{Size: artifact.Size}
		}
	}
	version, err := parseVersion(data, key, objects)
	if err != nil {
		t.Fatal(err)
	}
	if version.SchemaVersion != manifestSchemaVersion || len(version.Formats) != 2 || len(version.ManifestDigest) != 64 {
		t.Fatalf("unexpected version: %+v", version)
	}
}
