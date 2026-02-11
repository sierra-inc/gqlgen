package rewrite

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Rewriter struct {
	fset   *token.FileSet
	syntax []*ast.File
	files  map[string]string
	copied map[ast.Decl]bool
}

func New(dir string) (*Rewriter, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("unable to get absolute path for %q: %w", dir, err)
	}
	fset := token.NewFileSet()
	pkgMap, err := parser.ParseDir(fset, absDir, func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing directory %q: %w", absDir, err)
	}
	var syntax []*ast.File
	for _, pkg := range pkgMap {
		for _, f := range pkg.Files {
			syntax = append(syntax, f)
		}
	}
	return &Rewriter{
		fset:   fset,
		syntax: syntax,
		files:  map[string]string{},
		copied: map[ast.Decl]bool{},
	}, nil
}

func (r *Rewriter) getSource(start, end token.Pos) string {
	startPos := r.fset.Position(start)
	endPos := r.fset.Position(end)

	if startPos.Filename != endPos.Filename {
		panic("cant get source spanning multiple files")
	}

	file := r.getFile(startPos.Filename)
	return file[startPos.Offset:endPos.Offset]
}

func (r *Rewriter) getFile(filename string) string {
	if _, ok := r.files[filename]; !ok {
		b, err := os.ReadFile(filename)
		if err != nil {
			panic(fmt.Errorf("unable to load file, already exists: %w", err))
		}

		r.files[filename] = string(b)
	}

	return r.files[filename]
}

func (r *Rewriter) GetPrevDecl(structname, methodname string) *ast.FuncDecl {
	for _, f := range r.syntax {
		for _, d := range f.Decls {
			d, isFunc := d.(*ast.FuncDecl)
			if !isFunc {
				continue
			}
			if d.Name.Name != methodname {
				continue
			}
			if d.Recv == nil || len(d.Recv.List) == 0 {
				continue
			}
			recv := d.Recv.List[0].Type
			if star, isStar := recv.(*ast.StarExpr); isStar {
				recv = star.X
			}
			ident, ok := recv.(*ast.Ident)
			if !ok {
				continue
			}
			if ident.Name != structname {
				continue
			}
			r.copied[d] = true
			return d
		}
	}
	return nil
}

func (r *Rewriter) GetMethodComment(structname, methodname string) string {
	d := r.GetPrevDecl(structname, methodname)
	if d != nil && d.Doc != nil {
		comments := make([]string, len(d.Doc.List))

		for i := range d.Doc.List {
			c := d.Doc.List[i].Text

			switch c[1] {
			case '/':
				//-style comment (no newline at the end)
				c = c[2:]
			case '*':
				/*-style comment */
				c = c[2 : len(c)-2]
			}

			comments[i] = c
		}

		return strings.Join(comments, "\n")
	}

	return ""
}

func (r *Rewriter) GetMethodBody(structname, methodname string) string {
	d := r.GetPrevDecl(structname, methodname)
	if d != nil {
		return r.getSource(d.Body.Pos()+1, d.Body.End()-1)
	}
	return ""
}

func (r *Rewriter) MarkStructCopied(name string) {
	for _, f := range r.syntax {
		for _, d := range f.Decls {
			d, isGen := d.(*ast.GenDecl)
			if !isGen {
				continue
			}
			if d.Tok != token.TYPE || len(d.Specs) == 0 {
				continue
			}

			spec, isTypeSpec := d.Specs[0].(*ast.TypeSpec)
			if !isTypeSpec {
				continue
			}

			if spec.Name.Name != name {
				continue
			}

			r.copied[d] = true
		}
	}
}

func (r *Rewriter) ExistingImports(filename string) []Import {
	filename, err := filepath.Abs(filename)
	if err != nil {
		panic(err)
	}
	for _, f := range r.syntax {
		pos := r.fset.Position(f.Pos())

		if filename != pos.Filename {
			continue
		}

		var imps []Import
		for _, i := range f.Imports {
			name := ""
			if i.Name != nil {
				name = i.Name.Name
			}
			path, err := strconv.Unquote(i.Path.Value)
			if err != nil {
				panic(err)
			}
			imps = append(imps, Import{name, path})
		}
		return imps
	}
	return nil
}

func (r *Rewriter) RemainingSource(filename string) string {
	filename, err := filepath.Abs(filename)
	if err != nil {
		panic(err)
	}
	for _, f := range r.syntax {
		pos := r.fset.Position(f.Pos())

		if filename != pos.Filename {
			continue
		}

		var buf bytes.Buffer

		for _, d := range f.Decls {
			if r.copied[d] {
				continue
			}

			if d, isGen := d.(*ast.GenDecl); isGen && d.Tok == token.IMPORT {
				continue
			}

			buf.WriteString(r.getSource(d.Pos(), d.End()))
			buf.WriteString("\n")
		}

		return strings.TrimSpace(buf.String())
	}
	return ""
}

type Import struct {
	Alias      string
	ImportPath string
}
