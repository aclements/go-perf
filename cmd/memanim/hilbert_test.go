// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "testing"

func TestHilbert(t *testing.T) {
	const n = 64
	x0, y0 := hilbert(n, 0)
	have := make([]bool, n*n)
	have[x0+y0*n] = true
	for d := 1; d < n*n; d++ {
		x1, y1 := hilbert(n, d)
		if !(x0 == x1 && abs(y0-y1) == 1 ||
			y0 == y1 && abs(x0-x1) == 1) {
			t.Fatalf("moved by more than 1: (%d,%d) -> (%d,%d)", x0, y0, x1, y1)
		}
		if have[x1+y1*n] {
			t.Fatalf("repeated point (%d,%d)", x1, y1)
		}
		have[x1+y1*n] = true
		x0, y0 = x1, y1
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
