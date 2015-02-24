// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scale

import "math"

type Log struct {
	min, max, base float64
	logMin, denom  float64
}

// NewLog returns a new logarithmic scale.
//
// base has no effect on the scaling.  It is only used for computing
// tick marks.
func NewLog(input []float64, base float64) *Log {
	min, max := minmax(input)
	s := &Log{min: min, max: max, base: base}
	s.precompute()
	return s
}

func (s *Log) precompute() {
	s.logMin = math.Log(s.min)
	s.denom = math.Log(s.max) - s.logMin
}

func (s *Log) Of(x float64) float64 {
	return (math.Log(x) - s.logMin) / s.denom
}

// Nice expands the domain of s to "nice" values of the scale, which
// will translate into major tick marks.
//
// n is the maximum number of major ticks.  n must be >= 2.
func (s *Log) Nice(n int) {
	if n < 2 {
		panic("n must be >= 2")
	}

	// Increase the effective base until there are <= n major ticks
	for ebase := s.base; ; ebase *= s.base {
		// TODO: Allow for some round-off error

		// Compute major tick below s.min and above s.max
		lo := math.Pow(ebase, math.Floor(math.Log(s.min)/math.Log(ebase)))
		hi := math.Pow(ebase, math.Ceil(math.Log(s.max)/math.Log(ebase)))

		// Compute number of ticks between lo and hi
		nticks := 1 + (math.Log(hi)-math.Log(lo))/math.Log(ebase)

		if nticks <= float64(n) {
			// Found it
			s.min, s.max = lo, hi
			s.precompute()
			break
		}
	}
}

func (s *Log) Ticks(n int) (major, minor []float64) {
	if n < 2 {
		panic("n must be >= 2")
	}

	major, minor = []float64{}, []float64{}

	// Increase the effective base until there are <= n major ticks
	ebase := s.base
	for ; ; ebase *= s.base {
		// Compute number of ticks between lo and hi
		nticks := 1 + (math.Log(s.max)-math.Log(s.min))/math.Log(ebase)

		if nticks <= float64(n) {
			// Found it
			break
		}
	}

	// Start at the major tick below s.min
	x := math.Pow(ebase, math.Floor(math.Log(s.min)/math.Log(ebase)))
	for x <= s.max {
		for step := 0.0; step < ebase; step += ebase / s.base {
			x2 := x + step*x
			if x2 < s.min {
				continue
			} else if x2 > s.max {
				break
			}

			if step == 0 {
				major = append(major, x2)
			} else {
				minor = append(minor, x2)
			}
		}

		x *= ebase
	}

	return
}
