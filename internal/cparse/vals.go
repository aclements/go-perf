// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Extractor struct {
	Prologue string
	Names    []string
	Vals     map[string]interface{}
}

func (e *Extractor) Extract(env *BuildEnv) error {
	// Other ways to do this:
	//
	// __typeof__(x) xxx = x;
	//
	// Or generate inline assembly.
	//
	// Extract from object file (requires different object format
	// readers) or asm.

	// Construct printer program.
	src := bytes.NewBufferString(e.Prologue)
	src.WriteString(`
#include <stdio.h>
#include <string.h>

#define __CPARSE_PR(x) _Generic((x), \
	int                : __cparse_pr_int,  \
	long               : __cparse_pr_int,  \
	long long          : __cparse_pr_int,  \
	unsigned int       : __cparse_pr_uint,  \
	unsigned long      : __cparse_pr_uint, \
	unsigned long long : __cparse_pr_uint, \
	char*    : __cparse_pr_str)(x)
void __cparse_pr_int(long long x) {
	printf("int %lld\n", x);
}
void __cparse_pr_uint(unsigned long long x) {
	printf("uint %llu\n", x);
}
void __cparse_pr_str(const char *x) {
	printf("str %zu %s\n", strlen(x), x);
}

int main(int argc, char **argv) {
`)
	for _, n := range e.Names {
		fmt.Fprintf(src, "__CPARSE_PR(%s);\n", n)
	}
	src.WriteString("return 0;\n}\n")

	// Compiler printer.
	tdir, err := ioutil.TempDir("", "cparse-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tdir)
	prPath := filepath.Join(tdir, "cparse-pr.exe")

	ccArgs := append([]string(nil), env.CCArgs...)
	ccArgs = append(ccArgs, "-Wformat", "-Werror", "-std=c11", "-x", "c", "-o", prPath, "-")
	cc := exec.Command("cc", ccArgs...)
	cc.Stdin = src
	cc.Stderr = os.Stderr
	if _, err := cc.Output(); err != nil {
		fmt.Fprintf(os.Stderr, "compiling source:\n%s", src)
		return err
	}

	// Run printer.
	pr := exec.Command(prPath)
	outb, err := pr.Output()
	if err != nil {
		return err
	}
	out := string(outb)

	// Parse printer output.
	e.Vals = make(map[string]interface{})
	for i := 0; len(out) > 0; i++ {
		sep := strings.Index(out, " ")
		typ := out[:sep]
		out = out[sep+1:]
		switch typ {
		case "int":
			sep = strings.Index(out, "\n")
			val, err := strconv.Atoi(out[:sep])
			if err != nil {
				panic(err)
			}
			out = out[sep+1:]
			e.Vals[e.Names[i]] = val
		case "uint":
			sep = strings.Index(out, "\n")
			val, err := strconv.ParseUint(out[:sep], 10, 0)
			if err != nil {
				panic(err)
			}
			out = out[sep+1:]
			e.Vals[e.Names[i]] = uint(val)
		case "str":
			panic("not implemented: str")
		default:
			panic("unexpected type " + typ)
		}
	}

	return nil
}
