package interaction

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCoreAndApplicationPackagesDoNotDependOnTelegram(t *testing.T) {
	core := []string{
		"application", "domain", "clusterstate", "consensus", "nodecontrol", "runtimehost",
		"sessioncontrol", "sessionstart",
	}
	for _, name := range core {
		assertPackageImportsExclude(t, name, "/internal/telegram")
	}
}

func TestTelegramViewDoesNotDependOnTransportOrOrchestration(t *testing.T) {
	assertPackageImportsExclude(
		t, "telegramview", "/internal/telegramapp", "/internal/telegrambot",
	)
}

func TestTelegramOutboundDoesNotDependOnApplicationOrCardOrchestration(t *testing.T) {
	assertPackageImportsExclude(
		t, "telegramoutbound", "/internal/application", "/internal/domain",
		"/internal/telegramapp", "/internal/telegramview",
	)
}

func assertPackageImportsExclude(t *testing.T, name string, forbidden ...string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	packageDir := filepath.Join(root, "internal", name)
	files := token.NewFileSet()
	productionFile := func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}
	packages, err := parser.ParseDir(files, packageDir, productionFile, parser.ImportsOnly)
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
				for _, prefix := range forbidden {
					if strings.Contains(path, prefix) {
						position := files.Position(spec.Pos())
						t.Errorf("package %s imports forbidden dependency %s at %s", name, path, position)
					}
				}
			}
		}
	}
}
