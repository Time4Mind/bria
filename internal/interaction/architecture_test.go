package interaction

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCorePackagesDoNotDependOnTelegram(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	core := []string{
		"domain", "clusterstate", "consensus", "nodecontrol", "runtimehost",
		"sessioncontrol", "sessionstart",
	}
	for _, name := range core {
		packageDir := filepath.Join(root, "internal", name)
		files := token.NewFileSet()
		packages, err := parser.ParseDir(files, packageDir, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, pkg := range packages {
			for source, file := range pkg.Files {
				for _, spec := range file.Imports {
					path, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						t.Fatalf("decode import in %s: %v", source, err)
					}
					if strings.Contains(path, "/internal/telegram") {
						position := files.Position(spec.Pos())
						t.Errorf("core package %s imports Telegram adapter at %s", name, position)
					}
				}
			}
		}
	}
}
