package provision

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestDistributeCPUs(t *testing.T) {
	got := distributeCPUs(10, 3)
	want := []string{"0-3", "4-6", "7-9"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sets = %#v, want %#v", got, want)
		}
	}
}

func TestDistributeCPUTopologyKeepsSMTSiblingsAndNUMANodesTogether(t *testing.T) {
	var topology strings.Builder
	for cpu := 0; cpu < 104; cpu++ {
		node := 0
		core := cpu
		if cpu >= 52 {
			core -= 52
		}
		if core >= 26 {
			node = 1
			core -= 26
		}
		fmt.Fprintf(&topology, "%d,%d,%d,%d,Y\n", cpu, node, node, core)
	}
	threads := parseCPUTopology(topology.String())
	got := distributeCPUTopology(threads, 8)
	want := []string{
		"0-6,52-58", "7-13,59-65", "14-19,66-71", "20-25,72-77",
		"26-32,78-84", "33-39,85-91", "40-45,92-97", "46-51,98-103",
	}
	if len(got) != len(want) {
		t.Fatalf("sets = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sets = %#v, want %#v", got, want)
		}
	}
}

func TestParseCPUTopologyUsesActualOnlineCPUIds(t *testing.T) {
	threads := parseCPUTopology("# comment\n0,0,0,0,Y\n1,0,0,0,N\n2,0,0,1,Y\n")
	got := distributeCPUTopology(threads, 2)
	if len(got) != 2 || got[0] != "0" || got[1] != "2" {
		t.Fatalf("sets = %#v, want [0 2]", got)
	}
}

func TestParseGPUList(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{name: "nvidia list", output: "GPU 0: NVIDIA A10\nGPU 1: NVIDIA A10\n", want: 2},
		{name: "generic device labels", output: "Device 0 Accelerator\nDevice 1 Accelerator\n", want: 2},
		{name: "denglin table", output: "| GPU  Name |\n| 0  DL |\n| 1  DL |\n", want: 2},
		{name: "nvidia table", output: "| NVIDIA-SMI 560 |\n| 0  NVIDIA A10  Off |\n| 1  NVIDIA A10  Off |\n", want: 2},
		{name: "duplicate process rows", output: "| 0  DL |\n| 1  DL |\n| 0  12345 |\n", want: 2},
		{name: "non-contiguous noise", output: "| 0  DL |\n| 123  process |\n", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseGPUList(tt.output); got != tt.want {
				t.Fatalf("parseGPUList() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseDenglinPhysicalGPUCount(t *testing.T) {
	var output string
	for index := 0; index < 32; index++ {
		output += "Device " + strconv.Itoa(index) + " Denglin\n"
	}
	if got := parseDenglinPhysicalGPUCount(output); got != 8 {
		t.Fatalf("parseDenglinPhysicalGPUCount() = %d, want 8", got)
	}
	if got := parseDenglinPhysicalGPUCount("Device 0 Denglin\nDevice 1 Denglin\n"); got != 0 {
		t.Fatalf("incomplete logical device group returned %d, want 0", got)
	}
}

func TestParseDenglinDeviceNodes(t *testing.T) {
	if got := parseDenglinDeviceNodes("dl0\ndl1\ndl2\ndl3\ndl4\ndl5\ndl6\ndl7\n"); got != 2 {
		t.Fatalf("parseDenglinDeviceNodes() = %d, want 2", got)
	}
}

func TestNormalizeDenglinLegacyCount(t *testing.T) {
	if got, _ := normalizeDenglinLegacyCount("denglin", 32); got != 8 {
		t.Fatalf("legacy Denglin count = %d, want 8", got)
	}
	if got, _ := normalizeDenglinLegacyCount("denglin", 8); got != 8 {
		t.Fatalf("physical Denglin count = %d, want 8", got)
	}
	if got, _ := normalizeDenglinLegacyCount("nvidia", 32); got != 32 {
		t.Fatalf("NVIDIA count = %d, want 32", got)
	}
}

func TestParseSequentialIDs(t *testing.T) {
	if got := parseSequentialIDs("0\n1\n2\n"); got != 3 {
		t.Fatalf("parseSequentialIDs() = %d, want 3", got)
	}
	if got := parseSequentialIDs("warning 2026\n0\n1\n"); got != 2 {
		t.Fatalf("parseSequentialIDs() with warning = %d, want 2", got)
	}
}

func TestCountWorkerServices(t *testing.T) {
	output := "prometheus\ncenter-cv-service-0\ncenter-cv-service-1\ncenter-cv-service-1\n"
	if got := countWorkerServices(output); got != 2 {
		t.Fatalf("countWorkerServices() = %d, want 2", got)
	}
}

func TestCombineSSHOutput(t *testing.T) {
	if got := combineSSHOutput("128\n", "warning\n"); got != "128\nwarning\n" {
		t.Fatalf("combineSSHOutput() = %q", got)
	}
}

func TestParseNvidiaGPUInfo(t *testing.T) {
	got := parseNvidiaGPUInfo("NVIDIA L4, 23034\nNVIDIA L4, 23034\nNVIDIA A10, 22731\n")
	if len(got) != 2 || got[0].Model != "NVIDIA L4" || got[0].Count != 2 || got[0].MemoryBytes != 23034*1024*1024 {
		t.Fatalf("unexpected GPU details: %#v", got)
	}
	if gpuInfoCount(got) != 3 {
		t.Fatalf("GPU count = %d, want 3", gpuInfoCount(got))
	}
}

func TestParseNvidiaGPUInfoIgnoresInvalidRows(t *testing.T) {
	got := parseNvidiaGPUInfo("warning\nNVIDIA L4, unknown\n, 1024\n")
	if len(got) != 0 {
		t.Fatalf("unexpected GPU details: %#v", got)
	}
}
