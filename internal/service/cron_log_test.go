package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadLogChunkUTF8Boundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "第一行\n第二行 hello\n第一行\n第二行 hello\n第一行\n第二行 hello\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	total := int64(len(content))

	cases := []struct {
		name       string
		offset     *int64
		limit      *int64
		tail       bool
		want       string
		wantOffset int64
		wantNext   int64
		truncated  bool
	}{
		{"all", nil, nil, false, content, 0, total, false},
		{"tail", nil, int64Ptr(10), true, "行 hello\n", 68, total, true},
		{"offset", int64Ptr(5), int64Ptr(20), false, "行\n第二行 hello\n", 6, 26, true},
		{"split-rune", int64Ptr(0), int64Ptr(4), false, "第", 0, 3, true},
		{"past-end", int64Ptr(total + 10), int64Ptr(10), false, "", total, total, true},
	}
	for _, test := range cases {
		chunk, err := readLogChunk(path, test.offset, test.limit, test.tail)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if chunk.Content != test.want {
			t.Fatalf("%s content = %q, want %q", test.name, chunk.Content, test.want)
		}
		if chunk.Offset != test.wantOffset || chunk.NextOffset != test.wantNext || chunk.Total != total {
			t.Fatalf("%s offsets = %d/%d/%d, want %d/%d/%d", test.name,
				chunk.Offset, chunk.NextOffset, chunk.Total, test.wantOffset, test.wantNext, total)
		}
		if chunk.Truncated != test.truncated {
			t.Fatalf("%s truncated = %v, want %v", test.name, chunk.Truncated, test.truncated)
		}
	}
}

func TestReadLogChunkMissingFile(t *testing.T) {
	chunk, err := readLogChunk(filepath.Join(t.TempDir(), "missing.log"), nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Content != "" || chunk.Total != 0 || chunk.Truncated {
		t.Fatalf("unexpected chunk for missing file: %+v", chunk)
	}
}

func int64Ptr(value int64) *int64 { return &value }
