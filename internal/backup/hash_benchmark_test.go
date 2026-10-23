package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Representative of first scans and final verification of many small files.
func BenchmarkHashSmallFile(b *testing.B) {
	p := filepath.Join(b.TempDir(), "small.txt")
	if err := os.WriteFile(p, make([]byte, 4096), 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := HashFile(context.Background(), p); err != nil {
			b.Fatal(err)
		}
	}
}
