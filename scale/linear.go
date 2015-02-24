// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scale

type Linear struct {
	min, width float64
}

// NewLinear returns a new linear scale.
func NewLinear(input []float64) Linear {
	min, max := minmax(input)
	return Linear{min, max - min}
}

func (s Linear) Of(x float64) float64 {
	return (x - s.min) / s.width
}

func (s Linear) Ticks(n int) (major, minor []float64) {
	major, minor = make([]float64, n), []float64{}

	// TODO: Pick good ticks

	for i := range major {
		major[i] = float64(i)*s.width/float64(n) + s.min
	}

	return
}
