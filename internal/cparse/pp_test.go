// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"bytes"
	"os/exec"
	"testing"
)

var defaultEnv = BuildEnv{}

func TestMacros(t *testing.T) {
	needCC(t)

	src := bytes.NewBufferString("#define TEST 123\n#define EMPTY")
	macros, err := FindMacros(&defaultEnv, src)
	if err != nil {
		t.Fatal(err)
	}
outer:
	for _, want := range []string{"EMPTY", "TEST", "__STDC__"} {
		for _, have := range macros {
			if have == want {
				continue outer
			}
		}
		t.Errorf("%q is not defined", want)
	}
}

func needCC(t *testing.T) {
	t.Helper()

	const bin = "cc"
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("need %s binary in PATH", bin)
	}
}

func preprocess(t *testing.T, src string) []byte {
	t.Helper()

	sb := bytes.NewBufferString(src)
	pp, err := Preprocess(&defaultEnv, sb)
	if err != nil {
		t.Fatal(err)
	}
	return pp
}
