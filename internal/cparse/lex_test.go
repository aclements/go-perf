// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"testing"
)

func TestTokenize(t *testing.T) {
	needCC(t)

	pp := preprocess(t, "#include <stdio.h>")
	toks, err := Tokenize(pp)
	if err != nil {
		t.Fatal(err)
	}
	// Look for "int fprintf(FILE *".
	subSeq := []Tok{{TokKeyword, "int"}, {TokIdent, "fprintf"}, {TokOp, "("}, {TokIdent, "FILE"}, {TokOp, "*"}}
outer:
	for start := range toks {
		if len(toks)-start < len(subSeq) {
			t.Fatal("didn't find fprintf declaration in token stream")
		}
		for i, tok := range subSeq {
			if toks[start+i] != tok {
				continue outer
			}
		}
		// Found the subsequence.
		break
	}
}
