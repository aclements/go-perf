// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"image/color"

	"github.com/aclements/go-perf/scale"
)

type TicksFormat struct {
	tickLen, minorTickLen, textSep float64
	tickColor, labelColor          color.Color
	labelFormat                    string
}

func (f *TicksFormat) HTicks(svg *SVG, scale scale.Interface, x scale.OutputScale, y float64) {
	x.Crop()

	major, minor := scale.Ticks(5)

	// Draw ticks
	if f.tickColor == nil {
		svg.SetStroke(color.Black)
	} else {
		svg.SetStroke(f.tickColor)
	}
	svg.NewPath()
	for _, sx := range major {
		if x, ok := x.Of(scale.Of(sx)); ok {
			svg.MoveTo(x, y)
			svg.LineToRel(0, -f.tickLen)
		}
	}
	for _, sx := range minor {
		if x, ok := x.Of(scale.Of(sx)); ok {
			svg.MoveTo(x, y)
			svg.LineToRel(0, -f.minorTickLen)
		}
	}
	svg.Stroke()
	svg.SetStroke(nil)

	// Draw labels
	lOpts := TextOpts{Anchor: AnchorMiddle}
	if f.labelFormat != "" {
		if f.labelColor == nil {
			svg.SetFill(color.Black)
		} else {
			svg.SetFill(f.labelColor)
		}
		for _, sx := range major {
			if x, ok := x.Of(scale.Of(sx)); ok {
				l := fmt.Sprintf(f.labelFormat, sx)
				svg.Text(x, y-f.tickLen-f.textSep, lOpts, l)
			}
		}
		svg.SetFill(nil)
	}
}
