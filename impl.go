// impl generates method stubs for implementing an interface.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"golang.org/x/tools/imports"
)

var (
	flagSrcDir = flag.String("dir", "", "package source directory, useful for vendored code")
)

// findInterfaceOrStructure returns the import path and identifier of an interface or a structure.
// For example, given "http.ResponseWriter", findInterfaceOrStructure returns
// "net/http", "ResponseWriter".
// If a fully qualified interface or a structure is given, such as "net/http.ResponseWriter",
// it simply parses the input.
// If an unqualified interface or a structure such as "UserDefinedInterface" is given, then
// the definition is presumed to be in the package within srcDir and
// findInterfaceOrStructure returns "", "UserDefinedInterface".
func findInterfaceOrStructure(ifaceOrstruct string, srcDir string) (path string, id string, err error) {
	if len(strings.Fields(ifaceOrstruct)) != 1 {
		return "", "", fmt.Errorf("couldn't parse : %s", ifaceOrstruct)
	}

	srcPath := filepath.Join(srcDir, "__go_impl__.go")

	if slash := strings.LastIndex(ifaceOrstruct, "/"); slash > -1 {
		// package path provided
		dot := strings.LastIndex(ifaceOrstruct, ".")
		// make sure iface does not end with "/" (e.g. reject net/http/)
		if slash+1 == len(ifaceOrstruct) {
			return "", "", fmt.Errorf("name cannot end with a '/' character: %s", ifaceOrstruct)
		}
		// make sure iface does not end with "." (e.g. reject net/http.)
		if dot+1 == len(ifaceOrstruct) {
			return "", "", fmt.Errorf("name cannot end with a '.' character: %s", ifaceOrstruct)
		}
		// make sure iface has exactly one "." after "/" (e.g. reject net/http/httputil)
		if strings.Count(ifaceOrstruct[slash:], ".") != 1 {
			return "", "", fmt.Errorf("invalid name: %s", ifaceOrstruct)
		}
		return ifaceOrstruct[:dot], ifaceOrstruct[dot+1:], nil
	}

	src := []byte("package hack\n" + "var i " + ifaceOrstruct)
	// If we couldn't determine the import path, goimports will
	// auto fix the import path.
	imp, err := imports.Process(srcPath, src, nil)
	if err != nil {
		return "", "", fmt.Errorf("couldn't parse: %s", ifaceOrstruct)
	}

	// imp should now contain an appropriate import.
	// Parse out the import and the identifier.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, imp, 0)
	if err != nil {
		panic(err)
	}

	qualified := strings.Contains(ifaceOrstruct, ".")

	if len(f.Imports) == 0 && qualified {
		return "", "", fmt.Errorf("unrecognized: %s", ifaceOrstruct)
	}

	if !qualified {
		// If !qualified, the code looks like:
		//
		// package hack
		//
		// var i Reader
		decl := f.Decls[0].(*ast.GenDecl)      // var i io.Reader
		spec := decl.Specs[0].(*ast.ValueSpec) // i io.Reader
		sel := spec.Type.(*ast.Ident)
		id = sel.Name // Reader

		return path, id, nil
	}

	// If qualified, the code looks like:
	//
	// package hack
	//
	// import (
	//   "io"
	// )
	//
	// var i io.Reader
	raw := f.Imports[0].Path.Value   // "io"
	path, err = strconv.Unquote(raw) // io
	if err != nil {
		panic(err)
	}
	decl := f.Decls[1].(*ast.GenDecl)      // var i io.Reader
	spec := decl.Specs[0].(*ast.ValueSpec) // i io.Reader
	sel := spec.Type.(*ast.SelectorExpr)   // io.Reader
	id = sel.Sel.Name                      // Reader

	return path, id, nil
}

// Pkg is a parsed build.Package.
type Pkg struct {
	*build.Package
	*token.FileSet
}

// Spec is ast.TypeSpec with the associated comment map.
type Spec struct {
	*ast.TypeSpec
	ast.CommentMap
}

func typeMethods(path string, id string, srcDir string) ([]Func, error) {
	var pkg *build.Package
	var err error

	if path == "" {
		pkg, err = build.ImportDir(srcDir, 0)
		if err != nil {
			return nil, fmt.Errorf("couldn't find package in %s: %v", srcDir, err)
		}
	} else {
		pkg, err = build.Import(path, srcDir, 0)
		if err != nil {
			return nil, fmt.Errorf("couldn't find package %s: %v", path, err)
		}
	}

	fset := token.NewFileSet() // share one fset across the whole package
	p := Pkg{Package: pkg, FileSet: fset}
	var files []string
	files = append(files, pkg.GoFiles...)
	files = append(files, pkg.CgoFiles...)
	var funcs []Func
	for _, file := range files {
		f, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, file), nil, parser.ParseComments)
		if err != nil {
			continue
		}
		cmap := ast.NewCommentMap(fset, f, f.Comments)

		for _, decl := range f.Decls {
			fdecl, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}

			if fdecl == nil || fdecl.Recv == nil || len(fdecl.Recv.List) != 1 {
				continue
			}

			recv := fdecl.Recv.List[0]
			recvTyp, ok := recv.Type.(*ast.StarExpr)
			if !ok {
				continue
			}

			x, ok := recvTyp.X.(*ast.Ident)
			if !ok {
				continue
			}

			if x.Name != id {
				continue
			}

			if fdecl.Name.Name[0] > 'A' && fdecl.Name.Name[0] < 'Z' {
				funcs = append(funcs, p.parseFuncSig(fdecl.Name.Name, fdecl.Type, cmap.Filter(decl)))
			}
		}
	}
	return funcs, nil
}

// typeSpec locates the *ast.TypeSpec for type id in the import path.
func typeSpec(path string, id string, srcDir string) (Pkg, Spec, error) {
	var pkg *build.Package
	var err error

	if path == "" {
		pkg, err = build.ImportDir(srcDir, 0)
		if err != nil {
			return Pkg{}, Spec{}, fmt.Errorf("couldn't find package in %s: %v", srcDir, err)
		}
	} else {
		pkg, err = build.Import(path, srcDir, 0)
		if err != nil {
			return Pkg{}, Spec{}, fmt.Errorf("couldn't find package %s: %v", path, err)
		}
	}

	fset := token.NewFileSet() // share one fset across the whole package
	var files []string
	files = append(files, pkg.GoFiles...)
	files = append(files, pkg.CgoFiles...)
	for _, file := range files {
		f, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, file), nil, parser.ParseComments)
		if err != nil {
			continue
		}

		cmap := ast.NewCommentMap(fset, f, f.Comments)

		for _, decl := range f.Decls {
			decl, ok := decl.(*ast.GenDecl)
			if !ok || decl.Tok != token.TYPE {
				continue
			}
			for _, spec := range decl.Specs {
				spec := spec.(*ast.TypeSpec)
				if spec.Name.Name != id {
					continue
				}
				p := Pkg{Package: pkg, FileSet: fset}
				s := Spec{TypeSpec: spec, CommentMap: cmap.Filter(decl)}
				return p, s, nil
			}
		}
	}
	return Pkg{}, Spec{}, fmt.Errorf("type %s not found in %s", id, path)
}

// gofmt pretty-prints e.
func (p Pkg) gofmt(e ast.Expr) string {
	var buf bytes.Buffer
	printer.Fprint(&buf, p.FileSet, e)
	return buf.String()
}

// fullType returns the fully qualified type of e.
// Examples, assuming package net/http:
// 	fullType(int) => "int"
// 	fullType(Handler) => "http.Handler"
// 	fullType(io.Reader) => "io.Reader"
// 	fullType(*Request) => "*http.Request"
func (p Pkg) fullType(e ast.Expr) string {
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			// Using typeSpec instead of IsExported here would be
			// more accurate, but it'd be crazy expensive, and if
			// the type isn't exported, there's no point trying
			// to implement it anyway.
			if n.IsExported() {
				n.Name = p.Package.Name + "." + n.Name
			}
		case *ast.SelectorExpr:
			return false
		}
		return true
	})
	return p.gofmt(e)
}

func (p Pkg) params(field *ast.Field) []Param {
	var params []Param
	typ := p.fullType(field.Type)
	for _, name := range field.Names {
		params = append(params, Param{Name: name.Name, Type: typ})
	}
	// Handle anonymous params
	if len(params) == 0 {
		params = []Param{Param{Type: typ}}
	}
	return params
}

// Method represents a method signature.
type Method struct {
	Recv string
	Func
}

// Func represents a function signature.
type Func struct {
	Name     string
	Params   []Param
	Res      []Param
	Comments string
}

// Param represents a parameter in a function or method signature.
type Param struct {
	Name string
	Type string
}

func (p Pkg) funcsig(f *ast.Field, cmap ast.CommentMap) Func {
	typ := f.Type.(*ast.FuncType)

	return p.parseFuncSig(f.Names[0].Name, typ, cmap)
}

func (p Pkg) parseFuncSig(name string, typ *ast.FuncType, cmap ast.CommentMap) Func {
	fn := Func{Name: name}
	if typ.Params != nil {
		for _, field := range typ.Params.List {
			for _, param := range p.params(field) {
				// only for method parameters:
				// assign a blank identifier "_" to an anonymous parameter
				if param.Name == "" {
					param.Name = "_"
				}
				fn.Params = append(fn.Params, param)
			}
		}
	}
	if typ.Results != nil {
		for _, field := range typ.Results.List {
			fn.Res = append(fn.Res, p.params(field)...)
		}
	}

	comments := cmap.Comments()
	if len(comments) > 0 && (comments[0].Pos() < typ.Pos()) {
		fn.Comments = strings.TrimSuffix(flattenCommentMap(cmap), "\n")
	}

	return fn
}

// The error interface is built-in.
var errorInterface = []Func{{
	Name: "Error",
	Res:  []Param{{Type: "string"}},
}}

// funcs returns the set of methods required to implement iface.
// It is called funcs rather than methods because the
// function descriptions are functions; there is no receiver.
func funcs(iface string, srcDir string) ([]Func, error) {
	// Special case for the built-in error interface.
	if iface == "error" {
		return errorInterface, nil
	}

	// Locate the interface.
	path, id, err := findInterfaceOrStructure(iface, srcDir)
	if err != nil {
		return nil, err
	}

	// Parse the package and find the interface declaration.
	p, spec, err := typeSpec(path, id, srcDir)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %s", iface, err)
	}
	idecl, ok := spec.Type.(*ast.InterfaceType)
	if !ok {
		return nil, fmt.Errorf("not an interface: %s", iface)
	}

	if idecl.Methods == nil {
		return nil, fmt.Errorf("empty interface: %s", iface)
	}

	var fns []Func
	for _, fndecl := range idecl.Methods.List {
		if len(fndecl.Names) == 0 {
			// Embedded interface: recurse
			embedded, err := funcs(p.fullType(fndecl.Type), srcDir)
			if err != nil {
				return nil, err
			}
			fns = append(fns, embedded...)
			continue
		}

		fn := p.funcsig(fndecl, spec.CommentMap.Filter(fndecl))
		fns = append(fns, fn)
	}
	return fns, nil
}

// methods returns the set of structure's methods.
func methods(structure string, srcDir string) ([]Func, error) {
	// Locate the interface.
	path, id, err := findInterfaceOrStructure(structure, srcDir)
	if err != nil {
		return nil, err
	}

	return typeMethods(path, id, srcDir)
}

var tmplStub = template.Must(template.New("test").Parse(`
{{if .Comments}}{{.Comments}}{{end}}
func ({{.Recv}}) {{.Name}} ({{range .Params}}{{.Name}} {{.Type}}, {{end}}) ({{range .Res}}{{.Name}} {{.Type}}, {{end}}) {
	panic("not implemented") // TODO: Implement"
}
`))

var abstStub = template.Must(template.New("abst").Parse(`
type {{ .Name }} interface {
	{{ range .Funcs }}
		{{if .Comments}}{{.Comments}}{{end}}
		{{.Name}} ({{range .Params}}{{.Name}} {{.Type}}, {{end}}) ({{range .Res}}{{.Name}} {{.Type}}, {{end}})
	{{ end }}
}
`))

// genImplStubs prints nicely formatted method stubs
// for fns using receiver expression recv.
// If recv is not a valid receiver expression,
// genImplStubs will panic.
// genImplStubs won't generate stubs for
// already implemented methods of receiver.
func genImplStubs(recv string, fns []Func, implemented map[string]bool) []byte {
	var buf bytes.Buffer
	for _, fn := range fns {
		if implemented[fn.Name] {
			continue
		}
		meth := Method{Recv: recv, Func: fn}
		tmplStub.Execute(&buf, meth)
	}

	pretty, err := format.Source(buf.Bytes())
	if err != nil {
		panic(err)
	}
	return pretty
}

type interfaceDecl struct {
	Name  string
	Funcs []Func
}

func genInterfaceStubs(ifaceName string, fns []Func) []byte {
	var buf bytes.Buffer
	if err := abstStub.Execute(&buf, interfaceDecl{
		Name:  ifaceName,
		Funcs: fns,
	}); err != nil {
		panic(err)
	}

	pretty, err := format.Source(buf.Bytes())
	if err != nil {
		panic(err)
	}

	return pretty
}

// validReceiver reports whether recv is a valid receiver expression.
func validReceiver(recv string) bool {
	if recv == "" {
		// The parse will parse empty receivers, but we don't want to accept them,
		// since it won't generate a usable code snippet.
		return false
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "", "package hack\nfunc ("+recv+") Foo()", 0)
	return err == nil
}

// commentsBefore reports whether commentGroups precedes a field.
func commentsBefore(field *ast.Field, cg []*ast.CommentGroup) bool {
	if len(cg) > 0 {
		return cg[0].Pos() < field.Pos()
	}
	return false
}

// flattenCommentMap flattens the comment map to a string.
// This function must be used at the point when m is expected to have a single
// element.
func flattenCommentMap(m ast.CommentMap) string {
	// if len(m) != 1 {
	// 	panic("flattenCommentMap expects comment map of length 1")
	// }
	var result strings.Builder
	for _, cgs := range m {
		for _, cg := range cgs {
			for _, c := range cg.List {
				result.WriteString(c.Text)
				// add an end-of-line character if this is '//'-style comment
				if c.Text[1] == '/' {
					result.WriteString("\n")
				}
			}
		}
	}

	// for '/*'-style comments, make sure to append EOL character to the comment
	// block
	if s := result.String(); !strings.HasSuffix(s, "\n") {
		result.WriteString("\n")
	}

	return result.String()
}

func main() {
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, `
impl generates method stubs for recv to implement iface.

impl [-dir directory] <recv> <type> <name>

`[1:])
		flag.PrintDefaults()
		fmt.Fprint(os.Stderr, `

Examples:
		
impl 'f *File' io.Reader
impl Murmur hash.Hash
impl -dir $GOPATH/src/github.com/josharian/impl Murmur hash.Hash

Don't forget the single quotes around the receiver type
to prevent shell globbing.
`[1:])
		os.Exit(2)
	}
	flag.Parse()

	if len(flag.Args()) < 3 {
		flag.Usage()
	}

	recv, typ, name := flag.Arg(0), flag.Arg(1), flag.Arg(2)
	if !validReceiver(recv) {
		fatal(fmt.Sprintf("invalid receiver: %q", recv))
	}

	if *flagSrcDir == "" {
		if dir, err := os.Getwd(); err == nil {
			*flagSrcDir = dir
		}
	}

	switch typ {
	case "struct":
		fns, err := methods(name, *flagSrcDir)
		if err != nil {
			fatal(err)
		}

		src := genInterfaceStubs(recv, fns)
		fmt.Print(string(src))
	case "iface":
		fns, err := funcs(name, *flagSrcDir)
		if err != nil {
			fatal(err)
		}

		// Get list of already implemented funcs
		implemented, err := implementedFuncs(fns, recv, *flagSrcDir)
		if err != nil {
			fatal(err)
		}

		src := genImplStubs(recv, fns, implemented)
		fmt.Print(string(src))
	}
}

func fatal(msg interface{}) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
