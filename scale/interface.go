// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scale

// A scale satisfies Interface if it maps from some input range to an
// output interval [0, 1].
type Interface interface {
	Of(x float64) float64
	Ticks(n int) (major, minor []float64)
}
