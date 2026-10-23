package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestHashPooledBuffersConcurrentAndCancelled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		content := bytes.Repeat([]byte{byte(i + 1)}, 300000+i)
		path := filepath.Join(root, fmt.Sprint(i))
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		expected := fmt.Sprintf("%x", sha256.Sum256(content))
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			observed := Observe(ctx, func(e WorkEvent) {
				if e.Bytes > 0 {
					cancel()
				}
			})
			if _, err := HashFile(observed, path); err == nil {
				t.Fatal("cancelled hash succeeded")
			}
			for n := 0; n < 3; n++ {
				got, err := HashFile(context.Background(), path)
				if err != nil || got != expected {
					t.Fatalf("wrong concurrent digest: %s %v", got, err)
				}
			}
		})
	}
}
