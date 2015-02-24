// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scale

import "math"

type Power struct {
	lin Linear
	exp float64
}

// NewPower returns a new power scale.
func NewPower(input []float64, exp float64) Power {
	return Power{NewLinear(input), exp}
}

func (s Power) Of(x float64) float64 {
	return math.Pow(s.lin.Of(x), s.exp)
}

func (s Power) Ticks(n int) (major, minor []float64) {
	return s.lin.Ticks(n)
}
