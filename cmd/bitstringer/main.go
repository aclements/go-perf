// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command bitstringer generates String methods for bit-mask types.
//
// bitstringer is like the stringer tool, but for bit-mask types. See
// go doc stringer for details.
//
// bitstringer adds one flag, -strip, which specifies a prefix to
// strip from stringified-constants. For bit-mask types in particular,
// this can make the string representation much shorter, at the
// expense of not being unambiguous and syntactically valid Go code.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	flagTypes := flag.String("type", "", "comma-separated list of `types` to generate Stringers for")
	flagStrip := flag.String("strip", "", "strip `prefix` from constant names")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.PrintDefaults()
		os.Exit(2)
	}

	// Find source files.
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	pkg, err := build.ImportDir(wd, 0)
	if err != nil {
		log.Fatalf("importing %s: %v", wd, err)
	}
	paths := prefixDirectory(pkg.Dir, pkg.GoFiles)

	// Parse source files.
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			log.Fatalf("parsing file %s: %v", path, err)
		}
		files = append(files, f)
	}

	// Type check.
	conf := types.Config{Importer: importer.Default(), FakeImportC: true}
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
	}
	typesPkg, err := conf.Check(pkg.ImportPath, fset, files, info)
	if err != nil {
		log.Fatalf("checking package: %v", err)
	}
	scope := typesPkg.Scope()

	// Find the requested Types.
	names := strings.Split(*flagTypes, ",")
	name2Type := map[string]types.Type{}
	consts := map[types.Type][]*types.Const{}
	for _, name := range names {
		tname, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			log.Fatalf("unknown type %q", name)
		}
		// Check that it's integral.
		utype := tname.Type().Underlying()
		if utype, ok := utype.(*types.Basic); !ok || utype.Info()&types.IsInteger == 0 {
			log.Fatalf("type %q is not an integer type", name)
		}
		name2Type[name] = tname.Type()
		consts[tname.Type()] = nil
	}

	// Find all constants with each Type.
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		cobj, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		constList, ok := consts[cobj.Type()]
		if !ok {
			continue
		}
		constList = append(constList, cobj)
		consts[cobj.Type()] = constList
	}

	// Construct String methods.
	for _, name := range names {
		fname := strings.ToLower(name) + "_string.go"
		f, err := os.Create(fname)
		if err != nil {
			log.Fatalf("error creating %s: %v", fname, err)
		}
		typ := name2Type[name]
		writeStringer(f, pkg.Name, name, *flagStrip, consts[typ])
		if err := f.Close(); err != nil {
			log.Fatalf("error writing %s: %v", fname, err)
		}
	}
}

func prefixDirectory(dir string, names []string) []string {
	if dir == "." {
		return names
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = filepath.Join(dir, name)
	}
	return out
}

func writeStringer(w io.Writer, pkg, tname, prefix string, consts []*types.Const) {
	if len(consts) == 0 {
		fmt.Fprintf(os.Stderr, "warning: no consts for type %q\n", tname)
	}

	fmt.Fprintf(w, `// Code generated by "bitstringer -type=%s"; DO NOT EDIT

package %s

import "strconv"

func (i %s) String() string {
`, tname, pkg, tname)

	strip := func(s string) string {
		return strings.TrimPrefix(s, prefix)
	}

	// Find and format any zero value.
	zero := constant.MakeInt64(0)
	zlabel := "0"
	for _, c := range consts {
		val := c.Val()
		if constant.Compare(val, token.EQL, zero) {
			// Format it.
			zlabel = strip(c.Name())
			break
		}
	}
	fmt.Fprintf(w, "\tif i == 0 {\n\t\treturn %q\n\t}\n", zlabel)

	// Create bit value formatters.
	fmt.Fprintf(w, "\ts := \"\"\n")
	have := constant.MakeInt64(0)
	for _, c := range consts {
		// Does this contribute to the bit set?
		have2 := constant.BinaryOp(have, token.OR, c.Val())
		if constant.Compare(have, token.EQL, have2) {
			// Nope.
			continue
		}
		have = have2
		// Format it.
		fmt.Fprintf(w, "\tif i&%s != 0 {\n\t\ts += %q\n\t}\n", c.Name(), strip(c.Name())+"|")
	}
	// Handle any left-over bits.
	fmt.Fprintf(w, `	i &^= %s
	if i == 0 {
		return s[:len(s)-1]
	}
	return s + "0x" + strconv.FormatUint(uint64(i), 16)
}
`, have.ExactString())
}
