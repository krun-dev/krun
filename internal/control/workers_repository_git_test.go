package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newLocalGitRepo builds a real repository on disk with two commits so the git
// transport path is exercised end to end instead of a mocked HTTP API.
func newLocalGitRepo(t *testing.T, files map[string]string) (dir, head, first string) {
	t.Helper()
	dir = t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/index.ts", "export default {fetch(){return new Response('v1')}}")
	write("public/index.html", "<!doctype html><title>v1</title>")
	if _, err := worktree.Add("."); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "test", Email: "test@example.test", When: time.Now()}
	firstHash, err := worktree.Commit("first", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}
	first = firstHash.String()
	for name, body := range files {
		write(name, body)
	}
	if _, err := worktree.Add("."); err != nil {
		t.Fatal(err)
	}
	headHash, err := worktree.Commit("second", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}
	return dir, headHash.String(), first
}

// A local path is a valid git URL, so the clone path runs unchanged.
func localProvider(dir string) WorkerRepositoryProvider {
	return WorkerRepositoryProvider{Type: "gitlab", BaseURL: "file://" + dir, Priority: 10}
}

func TestCloneResolvesCommitAndReadsTree(t *testing.T) {
	dir, head, _ := newLocalGitRepo(t, map[string]string{
		"src/extra.ts":   "export const x = 1;",
		"wrangler.jsonc": `{"main":"src/index.ts","assets":{"directory":"./public"}}`,
	})
	provider := localProvider(dir)
	s := &Server{}

	commit, err := s.resolveRepositoryCommit(context.Background(), provider, WorkerRepositorySource{Repository: "", Ref: "master"}, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if commit != head {
		t.Fatalf("resolved %s, want %s", commit, head)
	}

	files, raw, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{
		WorkerRepositorySource: WorkerRepositorySource{Ref: "master"},
		Commit:                 head,
	}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, ok := files["wrangler.jsonc"]; !ok {
		t.Fatalf("config not read from clone: %v", keys(files))
	}
	if _, ok := files["src/index.ts"]; !ok {
		t.Fatalf("entrypoint not read from clone: %v", keys(files))
	}
	if len(raw) == 0 {
		t.Fatal("archive for asset extraction is empty")
	}
	// The .git directory must never leak into the build inputs.
	for name := range files {
		if strings.HasPrefix(name, ".git/") {
			t.Fatalf("git metadata leaked into source: %s", name)
		}
	}
}

func TestCloneHonoursRootDirectoryAndWrangler(t *testing.T) {
	dir, head, _ := newLocalGitRepo(t, map[string]string{
		"apps/demo/src/index.ts": "export default {fetch(){return new Response('demo')}}",
		"apps/demo/public/a.html": "<!doctype html><title>a</title>",
		"apps/demo/wrangler.jsonc": `{
  // comments and trailing commas are legal JSONC
  "main": "src/index.ts",
  "assets": { "directory": "./public", },
}`,
	})
	provider := localProvider(dir)
	s := &Server{}

	files, raw, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{
		WorkerRepositorySource: WorkerRepositorySource{Ref: "master", RootDirectory: "apps/demo"},
		Commit:                 head,
	}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, ok := files["wrangler.jsonc"]; !ok {
		t.Fatalf("config missing under root_directory: %v", keys(files))
	}
	cfg, _, err := parseWranglerConfig(files)
	if err != nil || cfg == nil {
		t.Fatalf("config parse: %v", err)
	}
	assets, entry, err := resolveWranglerBuild(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry != "src/index.ts" || assets.Directory != "public" {
		t.Fatalf("resolved %q %q", entry, assets.Directory)
	}
	assetFiles, err := extractRepositoryAssets(raw, "", assets.Directory)
	if err != nil || len(assetFiles) != 1 {
		t.Fatalf("assets: %v %v", keys(assetFiles), err)
	}
}

func TestCloneFailureIsReportedWithoutLeakingURL(t *testing.T) {
	s := &Server{}
	provider := WorkerRepositoryProvider{Type: "gitlab", BaseURL: "file:///nonexistent/repository"}
	_, err := s.resolveRepositoryCommit(context.Background(), provider, WorkerRepositorySource{Ref: "main"}, "")
	if err == nil {
		t.Fatal("missing repository accepted")
	}
	if strings.Contains(err.Error(), "/nonexistent/repository") {
		t.Fatalf("error leaks repository path: %v", err)
	}
}

func TestRepositoryCommitPinningSurvivesMove(t *testing.T) {
	dir, head, _ := newLocalGitRepo(t, map[string]string{"src/index.ts": "export default {fetch(){return new Response('v2')}}"})
	provider := localProvider(dir)
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Move the branch forward; a build pinned to `head` must still get that tree.
	worktree, _ := repo.Worktree()
	if err := os.WriteFile(filepath.Join(dir, "src/index.ts"), []byte("export default {fetch(){return new Response('v3')}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("src/index.ts"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "test", Email: "test@example.test", When: time.Now()}
	if _, err := worktree.Commit("third", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	s := &Server{}
	files, _, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{
		WorkerRepositorySource: WorkerRepositorySource{Ref: "master"},
		Commit:                 head,
	}, "")
	if err != nil {
		t.Fatalf("fetch pinned commit: %v", err)
	}
	if !strings.Contains(files["src/index.ts"], "v2") {
		t.Fatalf("pinned commit was not used: %s", files["src/index.ts"])
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
