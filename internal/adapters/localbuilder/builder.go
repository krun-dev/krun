package localbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

func Execute(ctx context.Context, config *appconfig.WorkersConfig, b domain.WorkerBuild, source, upload string, progress ...func(string)) (string, error) {
	dir, e := os.MkdirTemp("", "krun-local-build-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(dir)
	buildMode := "direct"
	configPath := ""
	assets := b.Assets
	if b.Repository != nil {
		buildMode = "wrangler"
		configPath = b.Repository.ConfigPath

		assets = nil
		if configPath == "" {
			return "", errors.New("repository build requires an explicit Wrangler config")
		}
	}
	input, e := json.Marshal(map[string]any{"build_mode": buildMode, "config_path": configPath, "entrypoint": b.Entrypoint, "source_url": source, "assets": assets, "compatibility_date": b.CompatibilityDate, "compatibility_flags": b.CompatibilityFlags, "upload_url": upload})
	if e != nil {
		return "", e
	}
	inputPath := filepath.Join(dir, "input.json")
	if e := os.WriteFile(inputPath, input, 0600); e != nil {
		return "", e
	}
	workDir := filepath.Join(dir, "work")
	if e := os.MkdirAll(workDir, 0700); e != nil {
		return "", e
	}
	tmpDir := filepath.Join(dir, "tmp")
	if e := os.MkdirAll(tmpDir, 0700); e != nil {
		return "", e
	}
	script := config.BuilderScript
	if script == "" {
		script = "workers/builder/build-worker.mjs"
	}
	script, e = filepath.Abs(script)
	if e != nil {
		return "", e
	}
	cmd := exec.CommandContext(ctx, "node", filepath.Base(script))
	cmd.Dir = filepath.Dir(script)
	cmd.Env = LocalBuilderEnvironment(inputPath, workDir, config.NPMRegistry, dir, tmpDir, config.BuilderTimeoutSeconds)
	output := &logBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if len(progress) > 0 {
					progress[0](output.snapshot(false))
				}
			}
		}
	}()
	runErr := cmd.Run()
	close(done)
	<-stopped
	log := output.snapshot(true)
	if len(log) > 32768 {
		log = log[:32768]
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return log, errors.New("local builder timed out")
		}
		return workerartifact.SafeWorkerLog(log + "\n" + runErr.Error()), runErr
	}
	return log, nil
}

func LocalBuilderEnvironment(inputPath, workDir, registry, homeDir, tmpDir string, timeoutSeconds int) []string {
	env := []string{
		"HOME=" + homeDir,
		"TMPDIR=" + tmpDir,
		"KRUN_BUILD_PHASE=all",
		"KRUN_BUILD_INPUT=" + inputPath,
		"KRUN_BUILD_WORK=" + workDir,
		"KRUN_NPM_REGISTRY=" + registry,
		"KRUN_BUILD_TIMEOUT_MS=" + appconfig.WorkerBuilderCommandTimeoutMillis(timeoutSeconds),
	}
	for _, key := range []string{
		"PATH", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// Bound memory even when a dependency emits unbounded output. Intermediate
// snapshots exclude partial lines so signed URLs split across writes stay private.
type logBuffer struct {
	mu   sync.Mutex
	text []byte
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := 32768 - len(b.text)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		b.text = append(b.text, p[:remaining]...)
	}
	return len(p), nil
}
func (b *logBuffer) snapshot(final bool) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(b.text)
	if !final {
		if end := strings.LastIndexByte(text, '\n'); end >= 0 {
			text = text[:end+1]
		} else {
			text = ""
		}
	}
	return workerartifact.SafeWorkerLog(text)
}
