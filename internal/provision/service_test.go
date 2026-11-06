package provision

import (
	"fmt"
	"strings"
	"testing"
)

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

// dlsmi 表格实机输出（dl-local 单卡 KS20，v2ulp.1c profile：1 个逻辑设备）。
const denglinSingleCardTable = `Fri Nov  6 16:40:08 2026
+------------------------------------------------------------------------------------------+
| DL-SMI 12.0                                  Driver version 2.3.55                       |
+------------------------------------------------------------------------------------------+
| GPU  Name                                     Bus-Id    Cluster-Memory-Usage    GPU-Util |
| Fan  Temp  Perf  Pwr:Usage/Cap          Memory-Usage     0     1     2     3      MIG M. |
|==========================================================================================|
|   0  KS20                               0000:01:00.0                                  0% |
| 50%   50C  P0         12W / 38W          289 / 16384   289     0     0     0    Disabled |
+------------------------------------------------------------------------------------------+

+------------------------------------------------------------------------------------------+
| Processes:                                                                               |
|     ID    TASK                                           0     1     2     3    Usage    |
|==========================================================================================|
|  No running processes found                                                              |
+------------------------------------------------------------------------------------------+
`

// 生产 8 卡机拓扑：32 个逻辑设备行分属 8 个 PCI 设备（每卡 4 个逻辑设备）。
func buildDenglinEightCardTable(t *testing.T) string {
	t.Helper()
	var table strings.Builder
	table.WriteString("Fri Nov  6 16:00:00 2026\n")
	for card := 0; card < 8; card++ {
		for device := 0; device < 4; device++ {
			index := card*4 + device
			fmt.Fprintf(&table, "| %2d  KS20  0000:%02x:00.0  0%% |\n", index, 1+card)
			fmt.Fprintf(&table, "| 50%%  50C  P0  12W / 38W  289 / 16384  Disabled |\n")
		}
	}
	return table.String()
}

func TestParseDenglinTableProbe(t *testing.T) {
	if physical, logical := parseDenglinTableProbe(denglinSingleCardTable); physical != 1 || logical != 1 {
		t.Fatalf("single-card table = (%d, %d), want (1, 1)", physical, logical)
	}
	if physical, logical := parseDenglinTableProbe(buildDenglinEightCardTable(t)); physical != 8 || logical != 32 {
		t.Fatalf("eight-card table = (%d, %d), want (8, 32)", physical, logical)
	}
	// 混插不同逻辑设备数的卡（5 个逻辑设备分属 3 个 PCI 设备）无法换算。
	mixed := "| 0  KS20  0000:01:00.0 |\n| 1  KS20  0000:01:00.0 |\n| 2  KS20  0000:02:00.0 |\n| 3  KS20  0000:03:00.0 |\n| 4  KS20  0000:03:00.0 |\n"
	if physical, logical := parseDenglinTableProbe(mixed); physical != 0 || logical != 0 {
		t.Fatalf("mixed topology = (%d, %d), want (0, 0)", physical, logical)
	}
	// 设备清单输出（dlsmi -L 形态）没有 PCI 地址，表格解析不采纳。
	if physical, _ := parseDenglinTableProbe("GPU 0: KS20 (profile: v2ulp.1c.1t.70)\n"); physical != 0 {
		t.Fatalf("list output should not be accepted by the table parser, got %d", physical)
	}
}

func TestParseDenglinListProbe(t *testing.T) {
	var eightCard strings.Builder
	for index := 0; index < 32; index++ {
		fmt.Fprintf(&eightCard, "GPU %d: KS20 (profile: 4c) (UUID: GPU-%032d)\n", index, index)
	}
	if physical, logical := parseDenglinListProbe(eightCard.String()); physical != 8 || logical != 32 {
		t.Fatalf("eight-card list = (%d, %d), want (8, 32)", physical, logical)
	}
	single := "GPU 0: KS20 (profile: v2ulp.1c.1t.70) (UUID: GPU-cd389c17-9d6c-d178-da04-1a194030da44)\n"
	if physical, logical := parseDenglinListProbe(single); physical != 1 || logical != 1 {
		t.Fatalf("single-card list = (%d, %d), want (1, 1)", physical, logical)
	}
	// 2 个逻辑设备（可能是 2 卡各 1 设备，也可能是 1 卡 2 设备）无法可靠换算。
	if physical, logical := parseDenglinListProbe("GPU 0: KS20\nGPU 1: KS20\n"); physical != 0 || logical != 0 {
		t.Fatalf("ambiguous list = (%d, %d), want (0, 0)", physical, logical)
	}
}

func TestParseDenglinDeviceNodes(t *testing.T) {
	// 驱动 2.3.55 节点命名为 dlgpuN（dl-local 实测），控制节点无数字后缀被跳过。
	if physical, logical := parseDenglinDeviceNodes("dlgpu0\ndlgpu-ctrl\ndlgpu-shm\ndlgpu-uvm\n"); physical != 1 || logical != 1 {
		t.Fatalf("dlgpu nodes = (%d, %d), want (1, 1)", physical, logical)
	}
	if physical, logical := parseDenglinDeviceNodes("dl0\ndl1\ndl2\ndl3\ndl4\ndl5\ndl6\ndl7\n"); physical != 2 || logical != 8 {
		t.Fatalf("dl nodes = (%d, %d), want (2, 8)", physical, logical)
	}
}

func TestParseDenglinGPUInfo(t *testing.T) {
	single := "GPU 0: KS20 (profile: v2ulp.1c.1t.70) (UUID: GPU-cd389c17-9d6c-d178-da04-1a194030da44)\n"
	got := parseDenglinGPUInfo(single, 1, 1)
	if len(got) != 1 || got[0].Model != "登临 KS20" || got[0].Count != 1 {
		t.Fatalf("single-card info = %#v, want one 登临 KS20", got)
	}
	var eightCard strings.Builder
	for index := 0; index < 32; index++ {
		fmt.Fprintf(&eightCard, "GPU %d: KS20 (profile: 4c)\n", index)
	}
	if got = parseDenglinGPUInfo(eightCard.String(), 8, 32); len(got) != 1 || got[0].Count != 8 {
		t.Fatalf("eight-card info = %#v, want eight physical KS20", got)
	}
	// 逻辑设备数未知时不展示换算后的卡数，交由调用方回落通用条目。
	if got = parseDenglinGPUInfo(eightCard.String(), 8, 0); got != nil {
		t.Fatalf("unknown logical count should return nil, got %#v", got)
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
