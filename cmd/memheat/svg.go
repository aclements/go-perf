// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/xml"
	"fmt"
	"image/color"
	"io"
	"strconv"
	"strings"
)

type SVG struct {
	w   io.Writer
	err error

	fill, stroke string
	lineWidth    string
	clipPath     string

	id int

	path []string
}

func NewSVG(w io.Writer, width, height int) *SVG {
	s := &SVG{w: w}
	s.fprintf("<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\">\n", width, height)
	s.fprintf("<style>.hover { fill:rgba(0,0,0,0) } .hover:hover { stroke:#000 }</style>\n")
	s.NewPath()
	return s
}

type svglen float64

func (v svglen) String() string {
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

func colorToCSS(c color.Color) string {
	cc := color.NRGBAModel.Convert(c).(color.NRGBA)
	if cc.A == 0xff {
		return fmt.Sprintf("rgb(%d,%d,%d)", cc.R, cc.G, cc.B)
	}
	return fmt.Sprintf("rgba(%d,%d,%d,%f)", cc.R, cc.G, cc.B, float64(cc.A)/0xff)
}

func (s *SVG) fprintf(format string, a ...interface{}) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintf(s.w, format, a...)
}

func (s *SVG) SetFill(c color.Color) {
	if c == nil {
		s.fill = ""
	} else {
		s.fill = "fill:" + colorToCSS(c)
	}
}

func (s *SVG) SetStroke(c color.Color) {
	if c == nil {
		s.stroke = ""
	} else {
		s.stroke = "stroke:" + colorToCSS(c)
	}
}

func (s *SVG) SetLineWidth(lw float64) {
	s.lineWidth = fmt.Sprintf("stroke-width:%v", svglen(lw))
}

func (s *SVG) style(parts ...string) string {
	val, sep := "", ""
	for _, part := range parts {
		if part != "" {
			val += sep + part
			sep = ";"
		}
	}
	if val != "" {
		return " style=\"" + val + "\""
	} else {
		return ""
	}
}

func (s *SVG) NewPath() *SVG {
	s.path = []string{}
	return s
}

func (s *SVG) MoveTo(x, y float64) *SVG {
	s.path = append(s.path, fmt.Sprintf("M%v %v", svglen(x), svglen(y)))
	return s
}

func (s *SVG) LineToRel(xd, yd float64) *SVG {
	var op string
	if xd == 0 {
		op = fmt.Sprintf("v%v", svglen(yd))
	} else if yd == 0 {
		op = fmt.Sprintf("h%v", svglen(xd))
	} else {
		op = fmt.Sprintf("l%v %v", svglen(xd), svglen(yd))
	}
	s.path = append(s.path, op)
	return s
}

func (s *SVG) Rect(x, y, w, h float64) *SVG {
	return s.MoveTo(x, y).LineToRel(w, 0).LineToRel(0, h).LineToRel(-w, 0).ClosePath()
}

func (s *SVG) ClosePath() *SVG {
	s.path = append(s.path, "z")
	return s
}

func (s *SVG) pathData() string {
	return strings.Join(s.path, "")
}

func (s *SVG) Stroke() *SVG {
	s.fprintf("<path d=\"%s\"%s/>\n", s.pathData(), s.style(s.stroke, s.lineWidth, s.clipPath))
	return s.NewPath()
}

func (s *SVG) FillPreserve() *SVG {
	s.fprintf("<path d=\"%s\"%s/>\n", s.pathData(), s.style(s.fill, s.clipPath))
	return s
}

func (s *SVG) Fill() *SVG {
	return s.FillPreserve().NewPath()
}

func (s *SVG) FillStroke() *SVG {
	s.fprintf("<path d=\"%s\"%s/>\n", s.pathData(), s.style(s.fill, s.stroke, s.lineWidth, s.clipPath))
	return s.NewPath()
}

func (s *SVG) Clip() *SVG {
	s.fprintf("<clipPath id=\"i%d\"><path d=\"%s\"/></clipPath>", s.id, s.pathData())
	s.clipPath = fmt.Sprintf("clip-path:url(#i%d)", s.id)
	s.id++
	return s.NewPath()
}

func (s *SVG) ResetClip() *SVG {
	s.clipPath = ""
	return s
}

func (s *SVG) Tooltip(text string) *SVG {
	s.fprintf("<path d=\"%s\" fill=\"rgba(0,0,0,0)\"><title>", s.pathData())
	if s.err == nil {
		s.err = xml.EscapeText(s.w, []byte(text))
	}
	s.fprintf("</title></path>\n")
	return s.NewPath()
}

func (s *SVG) TooltipHighlight(text string) *SVG {
	s.fprintf("<path d=\"%s\" fill=\"rgba(0,0,0,0)\" class=\"hover\"><title>", s.pathData())
	if s.err == nil {
		s.err = xml.EscapeText(s.w, []byte(text))
	}
	s.fprintf("</title></path>\n")
	return s.NewPath()
}

type Anchor int

const (
	AnchorStart Anchor = iota
	AnchorMiddle
	AnchorEnd
)

type Baseline int

const (
	BaselineAuto Baseline = iota
	BaselineBaseline
	BaselineMiddle
)

type TextOpts struct {
	Anchor   Anchor
	Baseline Baseline
	Rotate   float64
	FontSize float64
}

func (s *SVG) Text(x, y float64, opts TextOpts, text string) {
	astr := map[Anchor]string{
		AnchorStart:  "",
		AnchorMiddle: " text-anchor=\"middle\"",
		AnchorEnd:    " text-anchor=\"end\"",
	}[opts.Anchor]
	bstr := map[Baseline]string{
		BaselineAuto:     "",
		BaselineBaseline: " dominant-baseline=\"baseline\"",
		BaselineMiddle:   " dominant-baseline=\"middle\"",
	}[opts.Baseline]
	rstr := ""
	if opts.Rotate != 0 {
		rstr = fmt.Sprintf(" transform=\"rotate(%v,%v,%v)\"", svglen(opts.Rotate), svglen(x), svglen(y))
	}
	fstr := ""
	if opts.FontSize != 0 {
		fstr = fmt.Sprintf(" font-size=\"%v\"", svglen(opts.FontSize))
	}
	close := ""
	if s.clipPath != "" {
		// Don't apply rotation to clip path
		s.fprintf("<g%s>", s.style(s.clipPath))
		close = "</g>"
	}
	s.fprintf("<text x=\"%v\" y=\"%v\"%s%s%s%s%s>", svglen(x), svglen(y), astr, bstr, rstr, fstr, s.style(s.fill))
	if s.err == nil {
		s.err = xml.EscapeText(s.w, []byte(text))
	}
	s.fprintf("</text>%s\n", close)
}

func (s *SVG) Done() error {
	s.fprintf("</svg>")
	return s.err
}
