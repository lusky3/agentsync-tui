package actions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

// exportedFuncNames lists exported top-level funcs of the package's
// non-test sources (reflect cannot enumerate package-level funcs).
func exportedFuncNames() []string {
	fset := token.NewFileSet()
	ents, err := os.ReadDir(".")
	if err != nil {
		panic(err)
	}
	var out []string
	for _, e := range ents {
		fn := e.Name()
		if e.IsDir() || !strings.HasSuffix(fn, ".go") || strings.HasSuffix(fn, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, fn, nil, 0)
		if err != nil {
			panic(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.IsExported() {
				out = append(out, fd.Name.Name)
			}
		}
	}
	return out
}
