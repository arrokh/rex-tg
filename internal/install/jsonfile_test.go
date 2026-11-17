package install

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAtomicCreateNeverReplacesAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup")
	const writers = 32
	start := make(chan struct{})
	results := make(chan string, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			body := fmt.Sprintf("backup from writer %d", i)
			<-start
			if err := writeAtomic(path, []byte(body), 0o600, false); err == nil {
				results <- body
			}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var winners []string
	for body := range results {
		winners = append(winners, body)
	}
	if len(winners) != 1 {
		t.Fatalf("successful creators = %d, want exactly one", len(winners))
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != winners[0] {
		t.Fatalf("backup = %q, %v; want %q", got, err, winners[0])
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}
