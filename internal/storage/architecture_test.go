package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Check package boundaries recursively so moving a dependency into a subpackage
// cannot bypass the same rules enforced on the top-level packages.
func TestStorageLayerBoundaries(t *testing.T) {
	sql := regexp.MustCompile(`(?i)^\s*(SELECT\s+(?:[0-9]+\b|[^;]+\bFROM\b)|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|WITH\s+\w+\s+AS|CREATE\s+TABLE|ALTER\s+TABLE|DROP\s+TABLE)\b`)
	for _, layer := range []string{"control", "storage", "domain", "iam", "authorization", "securetoken", "auth", "audit", "workers", "repository", "runtimeconfig", "resourcename", "config", "artifact", "adapters"} {
		var paths []string
		err := filepath.WalkDir(filepath.Join("..", layer), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil || len(paths) == 0 {
			t.Fatalf("read %s sources: %v", layer, err)
		}
		for _, path := range paths {
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				if layer != "storage" && (strings.HasPrefix(name, "github.com/jackc/pgx/") || name == "database/sql" || strings.HasPrefix(name, "github.com/uptrace/bun")) {
					t.Errorf("%s imports database driver %s", path, name)
				}
				if layer != "control" && layer != "adapters" && (strings.HasPrefix(name, "net/http") || strings.Contains(name, "/internal/control") || strings.Contains(name, "minio-go")) {
					t.Errorf("%s imports business/transport dependency %s", path, name)
				}
				if layer == "domain" || layer == "securetoken" {
					if strings.Contains(strings.Split(name, "/")[0], ".") {
						t.Errorf("%s imports non-standard dependency %s", path, name)
					}
				}
				if layer == "storage" && (strings.Contains(name, "/internal/iam") || strings.Contains(name, "/internal/authorization") || strings.Contains(name, "/internal/auth") || strings.Contains(name, "/internal/workers") || strings.Contains(name, "/internal/repository") || strings.Contains(name, "/internal/runtimeconfig") || strings.Contains(name, "cedar-policy")) {
					t.Errorf("%s depends on business or authorization implementation %s", path, name)
				}
				if layer == "iam" || layer == "authorization" {
					if strings.Contains(strings.Split(name, "/")[0], ".") &&
						name != "github.com/krun-dev/krun/internal/domain" &&
						!(layer == "iam" && (name == "github.com/krun-dev/krun/internal/authorization" || name == "github.com/krun-dev/krun/internal/securetoken" || name == "github.com/krun-dev/krun/internal/resourcename")) &&
						!(layer == "authorization" && name == "github.com/cedar-policy/cedar-go") {
						t.Errorf("%s imports dependency outside its layer: %s", path, name)
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if method, ok := call.Fun.(*ast.SelectorExpr); ok {
						switch method.Sel.Name {
						case "AutoMigrate", "NewCreateTable", "NewDropTable", "NewAddColumn", "NewDropColumn", "NewCreateIndex", "NewDropIndex", "NewTruncateTable":
							t.Errorf("%s uses automatic ORM schema management", set.Position(call.Pos()))
						}
					}
				}
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, _ := strconv.Unquote(literal.Value)
				if layer != "storage" && sql.MatchString(value) {
					t.Errorf("%s contains SQL outside storage", set.Position(literal.Pos()))
				}
				return true
			})
		}
	}
}

// IAM HTTP adapters may parse requests and enforce route permissions, but may
// not take business mutations back from the service into handlers.
func TestIAMHandlersDoNotMutateEntities(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "control", "*.go"))
	if err != nil {
		t.Fatal(err)
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
		isState := func(expr ast.Expr) bool {
			selector, ok := expr.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			id, ok := selector.X.(*ast.Ident)
			return ok && id.Name == "st"
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					index, ok := lhs.(*ast.IndexExpr)
					if isState(lhs) || (ok && isState(index.X)) {
						t.Errorf("%s mutates IAM state outside iam package", set.Position(lhs.Pos()))
					}
				}
			case *ast.IncDecStmt:
				if isState(node.X) {
					t.Errorf("%s mutates IAM revision outside iam package", set.Position(node.Pos()))
				}
			case *ast.CallExpr:
				if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "delete" && len(node.Args) > 0 && isState(node.Args[0]) {
					t.Errorf("%s deletes IAM state outside iam package", set.Position(node.Pos()))
				}
			}
			return true
		})
	}
}
