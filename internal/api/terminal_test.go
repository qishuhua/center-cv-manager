package api

import "testing"

func TestClampTerminalSize(t *testing.T) {
	tests := []struct {
		name       string
		cols, rows int
		wantCols   int
		wantRows   int
	}{
		{name: "normal", cols: 120, rows: 36, wantCols: 120, wantRows: 36},
		{name: "minimum", cols: 0, rows: 0, wantCols: 20, wantRows: 8},
		{name: "maximum", cols: 900, rows: 400, wantCols: 500, wantRows: 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cols, rows := clampTerminalSize(test.cols, test.rows)
			if cols != test.wantCols || rows != test.wantRows {
				t.Fatalf("clampTerminalSize(%d, %d) = (%d, %d), want (%d, %d)", test.cols, test.rows, cols, rows, test.wantCols, test.wantRows)
			}
		})
	}
}
