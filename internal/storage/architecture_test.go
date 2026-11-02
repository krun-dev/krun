package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Keep the persistence boundary enforceable after this refactor: control owns
// HTTP and orchestration, storage owns SQL/drivers, domain owns shared values.
func TestStorageLayerBoundaries(t *testing.T) {
	sql := regexp.MustCompile(`(?i)^\s*(SELECT\s+(?:[0-9]+\b|[^;]+\bFROM\b)|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|WITH\s+\w+\s+AS|CREATE\s+TABLE|ALTER\s+TABLE|DROP\s+TABLE)\b`)
	for _, layer := range []string{"control", "storage", "domain"} {
		paths, err := filepath.Glob(filepath.Join("..", layer, "*.go"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("read %s sources: %v", layer, err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				if layer != "storage" && (strings.HasPrefix(name, "github.com/jackc/pgx/") || name == "database/sql") {
					t.Errorf("%s imports database driver %s", path, name)
				}
				if layer != "control" && (name == "net/http" || strings.Contains(name, "/internal/control") || strings.Contains(name, "minio-go")) {
					t.Errorf("%s imports business/transport dependency %s", path, name)
				}
				if layer == "domain" && strings.Contains(name, "/internal/storage") {
					t.Errorf("%s depends on persistence", path)
				}
			}
			if layer == "storage" {
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, _ := strconv.Unquote(literal.Value)
				if sql.MatchString(value) {
					t.Errorf("%s contains SQL outside storage", set.Position(literal.Pos()))
				}
				return true
			})
		}
	}
}
