// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"fmt"
)

type Enum struct {
	Tag   Tok
	Ident Tok
}

// FindEnums finds top-level enumeration constants in toks.
func FindEnums(tokens []Tok) ([]Enum, error) {
	t := toks(tokens)
	var enums []Enum
	for len(t) > 0 {
		switch {
		case t.Try(TokOp, "{"):
			t.SkipBalanced("}")
		case t.Try(TokOp, "("):
			t.SkipBalanced(")")
		case t.Try(TokOp, "["):
			t.SkipBalanced("]")
		case t.Try(TokKeyword, "enum"):
			// Found an enum. Skip tag, if any.
			tag, _ := t.TryIdent()
			if t.Try(TokOp, "{") {
				for {
					if t.Try(TokOp, "}") {
						break
					}
					id, ok := t.TryIdent()
					if !ok {
						return nil, fmt.Errorf("expected identifier")
					}
					enums = append(enums, Enum{tag, id})
					// Consume initializer.
					if t.Try(TokOp, "=") {
						t.SkipBalanced(",", "}")
					}
					t.Try(TokOp, ",")
				}
			}
		default:
			t.Skip(1)
		}
	}
	return enums, nil
}
