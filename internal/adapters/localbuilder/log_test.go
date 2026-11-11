package localbuilder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestLocalBuildPublishesOutputBeforeExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "builder.mjs")
	source := `
console.log("build started");
setTimeout(() => {
  console.error("build finished");
}, 11000);
`
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	seen := false
	log, err := Execute(context.Background(), &config.WorkersConfig{BuilderScript: script, BuilderTimeoutSeconds: 60}, domain.WorkerBuild{}, domain.WorkerBuildPrivate{}, "", "", func(text string) {
		if !strings.Contains(text, "build started") || strings.Contains(text, "build finished") {
			t.Errorf("not a live snapshot: %q", text)
		}
		seen = true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen || !strings.Contains(log, "build finished") {
		t.Fatalf("seen=%t log=%q", seen, log)
	}
}

func TestLogBufferBoundsAndRedactsSnapshots(t *testing.T) {
	b := &logBuffer{}
	_, _ = b.Write([]byte("safe\nhttps://object.test/?X-Amz-"))
	if got := b.snapshot(false); got != "safe\n" {
		t.Fatal(got)
	}
	_, _ = b.Write([]byte("Signature=secret\n"))
	if strings.Contains(b.snapshot(false), "secret") {
		t.Fatal("signed URL leaked")
	}
	_, _ = b.Write([]byte(strings.Repeat("x", 100000)))
	if len(b.snapshot(true)) > 32768 {
		t.Fatal("unbounded snapshot")
	}
}
