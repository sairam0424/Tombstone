package clientip_test

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// serviceRoot is the service's module root, two levels above this package.
const serviceRoot = "../.."

// forwardedHeaders are the request headers only this package may read, in
// lower case.
var forwardedHeaders = map[string]bool{
	"x-forwarded-for": true,
	"x-real-ip":       true,
	"true-client-ip":  true,
}

// TestOnlyThisPackageReadsForwardedHeaders pins the rule in the package doc:
// no other code in the service reads a client-address header, RemoteAddr, or
// chi's RealIP middleware (which rewrites RemoteAddr from those headers).
// Every other reader has to call ClientIP, so the trust decision stays in one
// file. It parses the service's non-test sources, so comments that merely name
// a header are fine.
func TestOnlyThisPackageReadsForwardedHeaders(t *testing.T) {
	own, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0

	walkErr := filepath.WalkDir(serviceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			abs, absErr := filepath.Abs(path)
			if absErr != nil {
				return absErr
			}
			if abs == own || entry.Name() == "vendor" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		scanned++
		for _, offence := range clientAddressReads(fset, file) {
			t.Errorf("%s reads %s; call clientip.ClientIP instead so the proxy trust check applies", offence.position, offence.what)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scan %s: %v", serviceRoot, walkErr)
	}
	if scanned == 0 {
		t.Fatalf("found no Go sources under %s; the scan would pass vacuously", serviceRoot)
	}
}

type clientAddressRead struct {
	position string
	what     string
}

// clientAddressReads lists the places file reads a client-address header by
// name, or touches RemoteAddr or RealIP.
func clientAddressReads(fset *token.FileSet, file *ast.File) []clientAddressRead {
	var reads []clientAddressRead
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				break
			}
			if name, err := strconv.Unquote(n.Value); err == nil && forwardedHeaders[strings.ToLower(name)] {
				reads = append(reads, clientAddressRead{fset.Position(n.Pos()).String(), "the " + name + " header"})
			}
		case *ast.SelectorExpr:
			if n.Sel.Name == "RemoteAddr" || n.Sel.Name == "RealIP" {
				reads = append(reads, clientAddressRead{fset.Position(n.Pos()).String(), n.Sel.Name})
			}
		}
		return true
	})
	return reads
}

// serviceCopies are the services that each carry a copy of this package.
var serviceCopies = []string{"flag-api", "marketplace", "evaluator"}

// copiedFiles are the files that must match across the copies.
var copiedFiles = []string{"clientip.go", "clientip_test.go", "invariants_test.go"}

// ownImportPath matches a service's import path for this package, the only
// line that legitimately differs between copies.
var ownImportPath = regexp.MustCompile(`github\.com/tombstone/[a-z-]+/internal/clientip`)

// TestCopiesStayIdentical pins the "change all three together" rule: this
// package is the trust gate for three services and each builds from its own
// Docker context, so the copies cannot share a module. The files must match
// byte for byte, apart from the service's own import path. A sibling that has
// no copy yet (an earlier commit, or a checkout holding only one service) is
// skipped; this service's own copy is always compared, which proves the path
// to the siblings is right.
func TestCopiesStayIdentical(t *testing.T) {
	own, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	compared := 0

	for _, service := range serviceCopies {
		dir := filepath.Join("..", "..", "..", service, "internal", "clientip")
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolve %s: %v", dir, err)
		}
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) && abs != own {
			t.Logf("no copy in %s, skipping", service)
			continue
		}
		for _, name := range copiedFiles {
			want := readWithoutImportPath(t, name)
			got := readWithoutImportPath(t, filepath.Join(dir, name))
			if !bytes.Equal(want, got) {
				t.Errorf("%s differs from the copy in %s; change all three together", name, service)
			}
			compared++
		}
	}
	if compared == 0 {
		t.Fatal("compared no files")
	}
}

func readWithoutImportPath(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return ownImportPath.ReplaceAll(content, []byte("<module>/internal/clientip"))
}
