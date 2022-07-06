// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"unicode"

	"github.com/aclements/go-perf/internal/cparse"
)

var ccflags = flag.String("ccflags", "", "space-separated list of flags to pass to cc")

func main() {
	flag.Parse()

	// Process each Go source file.
	for _, path := range flag.Args() {
		process(path)
	}
}

func process(path string) {
	src, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		log.Fatal(err)
	}

	// Extract directives.
	var defs []*def
	csrc := new(bytes.Buffer)
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			sp := strings.IndexAny(c.Text, " \r\n\t\v\f")
			if sp < 0 {
				continue
			}
			cmd := c.Text[2:sp]
			if cmd == "gendefs:C" {
				fmt.Fprintf(csrc, "%s\n", c.Text[sp:len(c.Text)-2])
			} else if cmd == "gendefs" {
				defs = append(defs, parseDef(c))
			}
		}
	}
	// Attach declaration blocks to def directives.
	defsTodo := defs
	for _, decl := range f.Decls {
		// Skip decls before the next def.
		if len(defsTodo) == 0 {
			break
		}
		if decl.Pos() < defsTodo[0].Pos {
			continue
		}
		// Attach this decl.
		if decl, ok := decl.(*ast.GenDecl); !ok || decl.Tok != token.CONST {
			log.Fatalf("%s: def must be applied to const", defsTodo[0].Pos)
		}
		defsTodo[0].Decl = decl.(*ast.GenDecl)
		defsTodo = defsTodo[1:]
		// Check for more than one def per decl.
		if len(defsTodo) > 0 && defsTodo[0].Pos < decl.Pos() {
			log.Fatalf("%s: multiple defs for declaration", fset.Position(decl.Pos()))
		}
	}
	if len(defsTodo) > 0 {
		log.Fatalf("%s: def without a declaration", fset.Position(defsTodo[0].Pos))
	}

	// Get identifier names from C code.
	env := cparse.BuildEnv{CCArgs: strings.Fields(*ccflags)}
	pp, err := cparse.Preprocess(&env, bytes.NewBuffer(csrc.Bytes()))
	if err != nil {
		log.Fatal(err)
	}
	toks, err := cparse.Tokenize(pp)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: Macros aren't in source order. :( Maybe I need to
	// sort them by value? Or do my own pre-processor scan just to
	// get the names (and hope that ignoring other pre-processor
	// directives is okay)?
	macros, err := cparse.FindMacros(&env, bytes.NewBuffer(csrc.Bytes()))
	if err != nil {
		log.Fatal(err)
	}
	consts, err := cparse.FindEnums(toks)
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range macros {
		consts = append(consts, cparse.Enum{Ident: cparse.Tok{Text: m}})
	}

	// Extract values of defs.
	ex := cparse.Extractor{Prologue: csrc.String()}
	for _, def := range defs {
		for i, c := range consts {
			id := c.Ident.Text
			if def.OmitMax && i == len(consts)-1 && strings.HasSuffix(id, "_MAX") {
				continue
			}
			if def.Omit[id] {
				continue
			}
			_, ok1 := def.CTag(c.Tag.Text)
			_, ok2 := def.CIdent(id)
			if ok1 && ok2 {
				def.Names = append(def.Names, id)
				ex.Names = append(ex.Names, id)
			}
		}
		if def.OmitMax && len(def.Names) > 0 {
			if last := def.Names[len(def.Names)-1]; !strings.HasSuffix(last, "_MAX") {
				log.Fatalf("%s: final name is %s, expected *_MAX", fset.Position(def.Pos), last)
			}
			def.Names = def.Names[:len(def.Names)-1]
			ex.Names = ex.Names[:len(ex.Names)-1]
		}

		if len(def.Names) == 0 {
			log.Fatalf("%s: no constants match C name", fset.Position(def.Pos))
		}
	}

	if err := ex.Extract(&env); err != nil {
		log.Fatal(err)
	}

	// Replace decls.
	var edits []Edit
	filePos := func(pos token.Pos) int {
		return fset.Position(pos).Offset
	}
	for _, def := range defs {
		// Delete the const block.
		lparen := filePos(def.Decl.Lparen)
		rparen := filePos(def.Decl.Rparen)
		edit := Edit{Pos: lparen + 1, Del: rparen - lparen - 1}
		insert := new(bytes.Buffer)

		// Translate values to expressions.
		var vals []interface{}
		for _, name := range def.Names {
			vals = append(vals, ex.Vals[name])
		}

		// Clean up value sequence.
		valExprs := cleanVals(vals)

		// Collect comments on existing values. We do this
		// straight from the text to avoid depending on how
		// package ast attaches comments to nodes. This
		// maintains interstitial comments and other
		// formatting.
		docText := map[string][]byte{}
		lineText := map[string][]byte{}
		prevEnd := lparen + 2 // Skip newline
		for _, spec := range def.Decl.Specs {
			spec := spec.(*ast.ValueSpec)
			docEnd := filePos(spec.Pos())
			// Back up to the end of the previous line,
			// where the comment ended.
			for src[docEnd] != '\n' {
				docEnd--
			}
			docEnd++
			// Extract the doc text from before the spec.
			if prevEnd < docEnd {
				docText[spec.Names[0].Name] = src[prevEnd:docEnd]
			}

			// Extract the line text from after the spec.
			end := filePos(spec.End())
			lineLen := bytes.IndexByte(src[end:], '\n')
			if lineLen > 0 {
				lineText[spec.Names[0].Name] = src[end : end+lineLen]
			}
			prevEnd = end + lineLen + 1 // Skip newline
		}

		var prevType string
		for i, name := range def.Names {
			suff, _ := def.CIdent(name)
			goName := def.GoPrefix + cNameToGo(suff)

			// Emit doc comment.
			if text, ok := docText[goName]; ok {
				fmt.Fprintf(insert, "\n%s", text)
			}

			// Emit name.
			fmt.Fprintf(insert, "\n\t%s", goName)

			// Emit type.
			if def.GoType != prevType {
				fmt.Fprintf(insert, " %s", def.GoType)
				prevType = def.GoType
			}

			// Emit value.
			if valExprs[i] != nil {
				fmt.Fprintf(insert, "=")
				printer.Fprint(insert, fset, valExprs[i])
			}

			// Emit line comment.
			if text, ok := lineText[goName]; ok {
				fmt.Fprintf(insert, "%s", text)
			}
		}

		fmt.Fprintf(insert, "\n")
		edit.Insert = insert.Bytes()
		edits = append(edits, edit)
	}

	fmt.Printf("%s", format(DoEdit(src, edits)))
}

type def struct {
	CTag     func(string) (string, bool)
	CIdent   func(string) (string, bool)
	GoPrefix string
	GoType   string
	Omit     map[string]bool
	OmitMax  bool
	Pos      token.Pos
	Decl     *ast.GenDecl
	Names    []string
}

func parseDef(c *ast.Comment) *def {
	var d def
	args := strings.Fields(c.Text)[1:]

	// Extract flags, leaving only positional args.
	var pos []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		switch {
		case arg == "-omit":
			if len(args) < 1 {
				log.Fatalf("missing argument: -no")
			}
			if d.Omit == nil {
				d.Omit = make(map[string]bool)
			}
			d.Omit[args[0]] = true
			args = args[1:]
		case arg == "-omit-max":
			d.OmitMax = true
		case arg[0] == '-':
			log.Fatalf("unknown directive flag %s", arg)
		default:
			pos = append(pos, arg)
		}
	}

	if len(pos) < 2 || len(pos) > 3 {
		log.Fatalf("wrong number of directive arguments; expected 2 or 3")
	}
	if i := strings.Index(pos[0], "."); i < 0 {
		d.CTag = func(x string) (string, bool) { return x, true }
		d.CIdent = compileGlob(pos[0])
	} else {
		d.CTag = compileGlob(pos[0][:i])
		d.CIdent = compileGlob(pos[0][i+1:])
	}
	d.GoPrefix = pos[1]
	if len(pos) == 2 {
		d.GoType = pos[1]
	} else {
		d.GoType = pos[2]
	}
	d.Pos = c.Slash

	return &d
}

func compileGlob(glob string) func(string) (suff string, match bool) {
	if !strings.HasSuffix(glob, "*") {
		return func(x string) (string, bool) {
			return "", glob == x
		}
	}
	pfx := glob[:len(glob)-1]
	return func(x string) (string, bool) {
		if strings.HasPrefix(x, pfx) {
			return x[len(pfx):], true
		}
		return "", false
	}
}

func cleanVals(lits []interface{}) []ast.Expr {
	// Look for sequential patterns.
	isOffset := make([]bool, len(lits))
	offsets := make([]int, len(lits))
	isShift := make([]bool, len(lits))
	for i, lit := range lits {
		switch val := lit.(type) {
		case int:
			isOffset[i] = true
			offsets[i] = val - i
			isShift[i] = val == (1 << uint(i))
		case uint:
		default:
			log.Fatalf("unhandled constant type %T", lit)
		}
	}

	// Extract longest runs and create exprs.
	runLen := func(len int, fn func(int) bool) int {
		for i := 0; i < len; i++ {
			if !fn(i) {
				return i
			}
		}
		return len
	}
	intLit := func(v int) ast.Expr {
		return &ast.BasicLit{
			Kind:  token.INT,
			Value: fmt.Sprint(v),
		}
	}
	exprs := make([]ast.Expr, len(lits))
	iota := ast.NewIdent("iota")
	for i := 0; i < len(lits); {
		offsetRun := runLen(len(offsets)-i, func(j int) bool {
			return isOffset[i+j] && offsets[i+j] == offsets[i]
		})
		shiftRun := runLen(len(isShift)-i, func(j int) bool {
			return isShift[i+j]
		})
		if offsetRun > 1 && offsetRun >= shiftRun {
			// Run of iota offsets.
			if offsets[i] == 0 {
				exprs[i] = iota
			} else {
				exprs[i] = &ast.BinaryExpr{X: iota, Op: token.ADD, Y: intLit(offsets[i])}
			}
			i += offsetRun
		} else if shiftRun > 1 {
			// Run of iota shifts.
			exprs[i] = &ast.BinaryExpr{X: intLit(1), Op: token.SHL, Y: iota}
			i += shiftRun
		} else {
			// Singleton.
			switch val := lits[i].(type) {
			case int:
				exprs[i] = intLit(val)
			case uint:
				exprs[i] = &ast.BasicLit{
					Kind:  token.INT,
					Value: fmt.Sprintf("%#x", val),
				}
			}
			i++
		}
	}
	return exprs
}

var words = map[string]string{
	"CPU":  "CPU",
	"PMU":  "PMU",
	"TSC":  "TSC",
	"MTC":  "MTC",
	"CTC":  "CTC",
	"ID":   "ID",
	"PID":  "PID",
	"TID":  "TID",
	"IP":   "IP",
	"L1D":  "L1D",
	"L1I":  "L1I",
	"LL":   "LL",
	"DTLB": "DTLB",
	"ITLB": "ITLB",
	"BPU":  "BPU", // Branch prediction unit?
	"HW":   "HW",  // Hardware
	"TX":   "TX",  // Transaction
	"HV":   "HV",  // Hypervisor

	"CGROUP":  "CGroup",
	"CPUMODE": "CPUMode",
}

func cNameToGo(c string) string {
	var out []rune
	parts := strings.Split(c, "_")
	for _, part := range parts {
		if w, ok := words[part]; ok {
			out = append(out, []rune(w)...)
			continue
		}
		for i, r := range part {
			if i == 0 {
				out = append(out, unicode.ToTitle(r))
			} else {
				out = append(out, unicode.ToLower(r))
			}
		}
	}
	return string(out)
}

func format(src []byte) []byte {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "<output>", src, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s", src)
		log.Fatal(err)
	}

	cfg := printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}
	buf := new(bytes.Buffer)
	cfg.Fprint(buf, fset, f)
	return buf.Bytes()
}
