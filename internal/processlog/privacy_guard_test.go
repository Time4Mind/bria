package processlog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionStructuredLogsDoNotRenderRawErrors(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, subtree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, subtree), func(
			path string,
			entry fs.DirEntry,
			walkErr error,
		) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, ok := selector.X.(*ast.Ident)
				if !ok || packageName.Name != "processlog" {
					return true
				}
				if selector.Sel.Name == "Criticalf" {
					t.Errorf("%s uses legacy Criticalf instead of typed Failuref", path)
					return true
				}
				formatIndex := -1
				switch selector.Sel.Name {
				case "Detailf", "Servicef":
					formatIndex = 0
				case "Failuref", "Outcomef":
					formatIndex = 2
				}
				if formatIndex < 0 || formatIndex >= len(call.Args) {
					return true
				}
				literal, ok := call.Args[formatIndex].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				format, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr == nil && (strings.Contains(format, "%v") || strings.Contains(format, "error=")) {
					t.Errorf("%s renders raw error text in structured log", path)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
