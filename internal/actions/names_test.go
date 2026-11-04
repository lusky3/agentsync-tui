package actions

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// exportedFuncNames lists exported top-level funcs of the package's
// non-test sources (reflect cannot enumerate package-level funcs).
func exportedFuncNames() []string {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		panic(err)
	}
	var out []string
	for _, p := range pkgs {
		for fn, f := range p.Files {
			if len(fn) > 8 && fn[len(fn)-8:] == "_test.go" {
				continue
			}
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.IsExported() {
					out = append(out, fd.Name.Name)
				}
			}
		}
	}
	return out
}
