// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "sort"

type Edit struct {
	Pos    int    // Byte offset of edit
	Del    int    // Number of bytes to delete at Pos
	Insert []byte // Bytes to insert at Pos
}

func DoEdit(src []byte, edits []Edit) []byte {
	// Sort the edits in order.
	sort.SliceStable(edits, func(i, j int) bool {
		return edits[i].Pos < edits[j].Pos
	})

	// Check for overlapping edits.
	for i := 1; i < len(edits); i++ {
		if edits[i-1].Pos+edits[i-1].Del > edits[i].Pos {
			panic("overlapping edits")
		}
	}

	// Process edits.
	out := []byte{}
	srcPos := 0
	for _, edit := range edits {
		out = append(out, src[srcPos:edit.Pos]...)
		out = append(out, edit.Insert...)
		srcPos = edit.Pos + edit.Del
	}
	out = append(out, src[srcPos:]...)

	return out
}
