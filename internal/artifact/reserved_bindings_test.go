package artifact

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestReservedBindingsMatchRuntime(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate artifact test source")
	}

	runtimePath := filepath.Join(filepath.Dir(source), "..", "..", "workers", "runtime", "entry.js")
	contents, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}

	sets := regexp.MustCompile(`(?s)const (?:reservedBindings|magicBindings) = new Set\(\[(.*?)\]\);`).FindAllSubmatch(contents, -1)
	if len(sets) != 2 {
		t.Fatalf("expected two runtime binding sets, found %d", len(sets))
	}

	names := regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"`)
	runtimeNames := map[string]bool{}
	for _, set := range sets {
		for _, match := range names.FindAllSubmatch(set[1], -1) {
			runtimeNames[string(match[1])] = true
		}
	}

	for name := range WorkerReservedBindings {
		if !runtimeNames[name] {
			t.Errorf("control plane reserves %q but runtime does not", name)
		}
	}
	for name := range runtimeNames {
		if !WorkerReservedBindings[name] {
			t.Errorf("runtime reserves %q but control plane does not", name)
		}
	}
}
