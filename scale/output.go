// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scale

type OutputScale struct {
	min, max float64
	clamp    int
}

const (
	clampCrop = iota
	clampNone
	clampClamp
)

func NewOutputScale(min, max float64) OutputScale {
	return OutputScale{min, max, clampCrop}
}

func (s *OutputScale) Crop() {
	s.clamp = clampCrop
}

func (s *OutputScale) Unclamp() {
	s.clamp = clampNone
}

func (s *OutputScale) Clamp() {
	s.clamp = clampClamp
}

func (s OutputScale) Of(x float64) (float64, bool) {
	if s.clamp == clampCrop {
		if x < 0 || x > 1 {
			return 0, false
		}
	} else if s.clamp == clampClamp {
		if x < 0 {
			x = 0
		} else if x > 1 {
			x = 1
		}
	}
	return x*(s.max-s.min) + s.min, true
}
