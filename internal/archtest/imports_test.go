package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/jpe0824/termchat/"

// goFile is one source file and the termchat packages it imports, both as
// paths relative to the module root.
type goFile struct {
	path    string // e.g. internal/cli/serve.go
	pkg     string // e.g. internal/cli
	test    bool
	imports []string // e.g. internal/model; "C" for cgo
}

func (f goFile) name() string { return filepath.Base(f.path) }

func sourceFiles(t *testing.T) []goFile {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var files []goFile
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (n == "testdata" || strings.HasPrefix(n, ".") || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		f := goFile{path: rel, pkg: filepath.ToSlash(filepath.Dir(rel)), test: strings.HasSuffix(rel, "_test.go")}
		for _, imp := range parsed.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case p == "C":
				f.imports = append(f.imports, "C")
			case strings.HasPrefix(p, module):
				f.imports = append(f.imports, strings.TrimPrefix(p, module))
			}
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 50 {
		t.Fatalf("found only %d Go files; is the module root right?", len(files))
	}
	return files
}

// rule is checked against every file it applies to; it returns the import that
// breaks it, or "".
type rule struct {
	what    string
	applies func(goFile) bool
	refuses func(f goFile, imp string) bool
}

func inPkg(pkgs ...string) func(goFile) bool {
	return func(f goFile) bool { return slices.Contains(pkgs, f.pkg) }
}

func onlyFrom(allowed ...string) func(goFile, string) bool {
	return func(_ goFile, imp string) bool { return !slices.Contains(allowed, imp) }
}

func connectorPkg(imp string) bool {
	return imp == "internal/connector" || strings.HasPrefix(imp, "internal/connector/")
}

var rules = []rule{
	{
		what: "only server and connector/termchat import proto (testutil too, for tests)",
		applies: func(f goFile) bool {
			return !slices.Contains([]string{"internal/proto", "internal/server", "internal/connector/termchat", "internal/testutil"}, f.pkg)
		},
		refuses: func(_ goFile, imp string) bool { return imp == "internal/proto" },
	},
	{
		what:    "store imports only model",
		applies: func(f goFile) bool { return f.pkg == "internal/store" && !f.test },
		refuses: onlyFrom("internal/model"),
	},
	{
		what:    "proto and connector import only model",
		applies: func(f goFile) bool { return inPkg("internal/proto", "internal/connector")(f) && !f.test },
		refuses: onlyFrom("internal/model"),
	},
	{
		what:    "ipc and daemon never import proto or server",
		applies: inPkg("internal/ipc", "internal/daemon"),
		refuses: func(_ goFile, imp string) bool { return imp == "internal/proto" || imp == "internal/server" },
	},
	{
		what:    "the CLI's commands never import the store or a connector",
		applies: func(f goFile) bool { return f.pkg == "internal/cli" && !f.test },
		refuses: func(_ goFile, imp string) bool { return imp == "internal/store" || connectorPkg(imp) },
	},
	{
		what:    "only serve.go and server_admin.go import server in the CLI",
		applies: func(f goFile) bool { return f.pkg == "internal/cli" && !f.test },
		refuses: func(f goFile, imp string) bool {
			return imp == "internal/server" && f.name() != "serve.go" && f.name() != "server_admin.go"
		},
	},
	{
		what:    "the TUI talks to ipc and model, plus sanitize, config, paths and notify",
		applies: func(f goFile) bool { return f.pkg == "internal/tui" && !f.test },
		refuses: onlyFrom("internal/ipc", "internal/model", "internal/sanitize", "internal/config", "internal/paths", "internal/notify"),
	},
	{
		what:    "no cgo",
		applies: func(goFile) bool { return true },
		refuses: func(_ goFile, imp string) bool { return imp == "C" },
	},
}

func violations(files []goFile) []string {
	var out []string
	for _, r := range rules {
		for _, f := range files {
			if !r.applies(f) {
				continue
			}
			for _, imp := range f.imports {
				if r.refuses(f, imp) {
					out = append(out, f.path+" imports "+imp+": "+r.what)
				}
			}
		}
	}
	return out
}

func TestImportBoundaries(t *testing.T) {
	for _, v := range violations(sourceFiles(t)) {
		t.Error(v)
	}
}

// Each rule must catch the violation it names; a rule that never fires would
// let the test above pass on anything.
func TestEachRuleCatchesItsViolation(t *testing.T) {
	bad := []goFile{
		{path: "internal/daemon/x.go", pkg: "internal/daemon", imports: []string{"internal/proto"}},
		{path: "internal/cli/x_test.go", pkg: "internal/cli", test: true, imports: []string{"internal/proto"}},
		{path: "internal/store/x.go", pkg: "internal/store", imports: []string{"internal/sanitize"}},
		{path: "internal/connector/x.go", pkg: "internal/connector", imports: []string{"internal/store"}},
		{path: "internal/ipc/x.go", pkg: "internal/ipc", imports: []string{"internal/server"}},
		{path: "internal/cli/x.go", pkg: "internal/cli", imports: []string{"internal/store"}},
		{path: "internal/cli/y.go", pkg: "internal/cli", imports: []string{"internal/connector"}},
		{path: "internal/cli/z.go", pkg: "internal/cli", imports: []string{"internal/server"}},
		{path: "internal/tui/x.go", pkg: "internal/tui", imports: []string{"internal/store"}},
		{path: "internal/model/x.go", pkg: "internal/model", imports: []string{"C"}},
	}
	for _, f := range bad {
		if v := violations([]goFile{f}); len(v) == 0 {
			t.Errorf("%s importing %v passed every rule", f.path, f.imports)
		}
	}
	ok := []goFile{
		{path: "internal/cli/serve.go", pkg: "internal/cli", imports: []string{"internal/server"}},
		{path: "internal/cli/x_test.go", pkg: "internal/cli", test: true, imports: []string{"internal/server", "internal/store"}},
		{path: "internal/testutil/h.go", pkg: "internal/testutil", imports: []string{"internal/proto"}},
	}
	for _, f := range ok {
		if v := violations([]goFile{f}); len(v) != 0 {
			t.Errorf("allowed import refused: %v", v)
		}
	}
}
