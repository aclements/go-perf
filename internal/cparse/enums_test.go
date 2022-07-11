// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"testing"
)

func TestFindEnums(t *testing.T) {
	needCC(t)

	pp := preprocess(t, "int foo(); enum tag { A, B = 2 + (C, D), E };")
	toks, err := Tokenize(pp)
	if err != nil {
		t.Fatal(err)
	}
	enums, err := FindEnums(toks)
	if err != nil {
		t.Fatal(err)
	}
	if len(enums) != 3 {
		t.Fatalf("expected 3 enums, got %d", len(enums))
	}
	for i, name := range []string{"A", "B", "E"} {
		if enums[i].Tag.Text != "tag" || enums[i].Ident.Text != name {
			t.Errorf("expected enum tag.%s, got %v", name, enums[i])
		}
	}
}
