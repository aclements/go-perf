// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"debug/elf"
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"path"
	"sort"

	"github.com/aclements/goperf/dwarfx"
	"github.com/aclements/goperf/perffile"
	"github.com/aclements/goperf/perfsession"
	"github.com/aclements/goperf/scale"
)

type lineStat struct {
	ip          uint64
	totalWeight uint64
	weights     []uint64

	fn  string
	src *dwarfx.LineEntry

	yCoord    float64
	histogram []int
}

func main() {
	var (
		flagInput = flag.String("i", "perf.data", "input perf.data file")
		flagLimit = flag.Int("limit", 30, "output top N functions")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(1)
	}

	f, err := perffile.Open(*flagInput)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	s := perfsession.New()

	// Collect samples by IP (TODO: by (comm, ip) or something)
	ipToInfo := map[uint64]*lineStat{}
	rs := f.Records()
	for rs.Next() {
		r := rs.Record
		s.Update(r)

		switch r := r.(type) {
		case *perffile.RecordSample:
			mmap := s.LookupPID(r.PID).LookupMmap(r.IP)
			if mmap == nil {
				break
			}

			extra := getMmapExtra(mmap)
			if extra == nil {
				break
			}

			line, ok := ipToInfo[r.IP]
			if !ok {
				fn, src := extra.findIP(r.IP)
				line = &lineStat{
					ip:  r.IP,
					fn:  fn,
					src: src,
				}
				ipToInfo[r.IP] = line
			}
			line.weights = append(line.weights, r.Weight)
			line.totalWeight += r.Weight
		}
	}

	// Compute total function weight
	fnWeight := map[string]uint64{}
	for _, ls := range ipToInfo {
		fnWeight[ls.fn] += ls.totalWeight
	}

	// Sort stats by function weight, line number
	stats := make([]*lineStat, 0, len(ipToInfo))
	for _, ls := range ipToInfo {
		stats = append(stats, ls)
	}
	sort.Sort(lineStatSorter{stats, fnWeight})

	// Limit to the top N functions
	if *flagLimit > 0 {
		stats = limitFuncs(stats, *flagLimit)
	}

	// Find max weight
	maxWeight := uint64(0)
	for _, stat := range stats {
		for _, w := range stat.weights {
			if w > maxWeight {
				maxWeight = w
			}
		}
	}
	wscale := scale.NewPower([]float64{0, float64(maxWeight)}, 1/2.0)

	// Compute histograms and find max bar height
	const buckets = 50
	lscale := scale.NewLog([]float64{1, float64(maxWeight + 1)}, 10)
	lscale.Nice(5)
	maxHeight := 0
	for _, stat := range stats {
		stat.histogram = make([]int, buckets)
		for _, w := range stat.weights {
			bucket := int(lscale.Of(float64(w)) * buckets)
			stat.histogram[bucket] += int(w)
		}
		for _, height := range stat.histogram {
			if height > maxHeight {
				maxHeight = height
			}
		}
	}

	// Assign Y coordinates
	const (
		marginTop      = 45
		cellHeight     = 10
		cellWidth      = 10
		fnGap          = 10
		lineLabelWidth = 30
		groupWidth     = 20
		groupGap       = 5

		marginLeft  = groupWidth*2 + groupGap
		marginRight = 300

		sourceLeft = marginLeft + buckets*cellWidth
	)
	y := marginTop
	// lastLine := -1
	for i, stat := range stats {
		if i != 0 && (stat.fn != stats[i-1].fn || stat.src.FileEntry.FileName != stats[i-1].src.FileEntry.FileName) {
			y += fnGap
			// lastLine = -1
		}
		// if lastLine != -1 {
		// 	y += cellHeight * (stat.src.Line - lastLine)
		// }
		stat.yCoord = float64(y)
		// lastLine = stat.src.Line
		y += cellHeight
	}

	// Emit SVG
	svg := NewSVG(os.Stdout, sourceLeft+marginRight, y)

	var ticks = TicksFormat{
		tickLen:      5,
		minorTickLen: 3,
		textSep:      5,
		labelFormat:  "%g",
	}

	// TODO: Show command line and hostname

	{
		lOpts := TextOpts{Anchor: AnchorMiddle}
		svg.SetFill(color.Black)
		y := float64(marginTop - ticks.tickLen - ticks.textSep)
		svg.Text(marginLeft+cellWidth*buckets/2, y-20, lOpts, "memory load latency (cycles)")
		svg.SetFill(nil)
	}

	// TODO: Draw color key

	// Label lines
	svg.NewPath()
	lastLineY := -1.0
	for _, idxs := range sections(len(stats), func(i int) bool {
		return stats[i].src.Line != stats[i-1].src.Line
	}) {
		first, last := stats[idxs[0]], stats[idxs[1]-1]
		top := first.yCoord
		bot := last.yCoord + cellHeight

		if lastLineY != top {
			svg.MoveTo(sourceLeft, top)
			svg.LineToRel(ticks.tickLen, 0)
		}

		svg.MoveTo(sourceLeft, bot)
		svg.LineToRel(ticks.tickLen, 0)
		lastLineY = bot

		lOpts := TextOpts{Anchor: AnchorStart, Baseline: BaselineMiddle, FontSize: 10}
		svg.Text(sourceLeft+ticks.tickLen, (top+bot)/2, lOpts, fmt.Sprintf("%d", first.src.Line))

		svg.Text(sourceLeft+lineLabelWidth, (top+bot)/2, lOpts, getLine(first.src.FileEntry.FileName, first.src.Line))
	}
	svg.SetStroke(color.Black)
	svg.Stroke()
	svg.SetStroke(nil)

	groupX := float64(marginLeft)

	// Label function groups
	for _, idxs := range sections(len(stats), func(i int) bool {
		return stats[i].fn != stats[i-1].fn
	}) {
		first, last := stats[idxs[0]], stats[idxs[1]-1]
		top := first.yCoord
		bot := last.yCoord + cellHeight

		// Ticks at the top of each function
		ticks.HTicks(svg, lscale, scale.NewOutputScale(marginLeft, marginLeft+cellWidth*buckets), top)
		ticks.labelFormat = ""

		// Function label
		lOpts := TextOpts{Anchor: AnchorMiddle, Rotate: -90}
		svg.SetFill(color.Gray{192})
		svg.Rect(groupX-groupWidth, top, groupWidth, bot-top).FillPreserve().Clip()
		svg.SetFill(color.Black)
		svg.Text(groupX-5, (top+bot)/2, lOpts, first.fn)
		svg.ResetClip()
	}
	groupX -= groupWidth + groupGap

	// Label file name groups
	for _, idxs := range sections(len(stats), func(i int) bool {
		return stats[i].src.FileEntry.FileName != stats[i-1].src.FileEntry.FileName
	}) {
		lOpts := TextOpts{Anchor: AnchorMiddle, Rotate: -90}
		svg.SetFill(color.Gray{192})
		top := stats[idxs[0]].yCoord
		bot := stats[idxs[1]-1].yCoord + cellHeight
		svg.Rect(groupX-groupWidth, top, groupWidth, bot-top).FillPreserve().Clip()
		svg.SetFill(color.Black)
		fileName := path.Base(stats[idxs[0]].src.FileEntry.FileName)
		svg.Text(groupX-5, (top+bot)/2, lOpts, fileName)
		svg.ResetClip()
	}
	groupX -= groupWidth + groupGap

	// Draw heat map
	for _, stat := range stats {
		for x, height := range stat.histogram {
			if height == 0 {
				continue
			}
			shade := wscale.Of(float64(height))
			svg.SetFill(color.NRGBA{255, 0, 0, uint8(255 * shade)})
			svg.Rect(float64(marginLeft+x*cellWidth), stat.yCoord,
				cellWidth, cellHeight).Fill()
		}

		// Tooltip for raw IP
		svg.Rect(marginLeft, stat.yCoord, cellWidth*buckets, cellHeight).TooltipHighlight(fmt.Sprintf("IP: %#x", stat.ip))
	}
	svg.SetFill(nil)
	svg.Done()
}

type lineStatSorter struct {
	lines    []*lineStat
	fnWeight map[string]uint64
}

func (s lineStatSorter) Len() int {
	return len(s.lines)
}

func (s lineStatSorter) Swap(i, j int) {
	s.lines[i], s.lines[j] = s.lines[j], s.lines[i]
}

func (s lineStatSorter) Less(i, j int) bool {
	// Sort by function weight first
	fni, fnj := s.lines[i].fn, s.lines[j].fn
	if s.fnWeight[fni] != s.fnWeight[fnj] {
		return s.fnWeight[fni] > s.fnWeight[fnj]
	}

	// Sort by file:line
	li, lj := s.lines[i].src, s.lines[j].src
	if li != nil || lj != nil {
		if li == nil || lj == nil {
			// Unknown line info comes first
			return li == nil
		}
		if li.FileEntry.FileName != lj.FileEntry.FileName {
			return li.FileEntry.FileName < lj.FileEntry.FileName
		}
		if li.Line != lj.Line {
			return li.Line < lj.Line
		}
	}

	// Finally, sort by IP
	return s.lines[i].ip < s.lines[j].ip
}

type mmapExtra struct {
	functab []funcRange
	linetab []*dwarfx.LineEntry
}

func (m *mmapExtra) Fork() perfsession.ForkableExtra {
	return m
}

func (m *mmapExtra) findIP(ip uint64) (fn string, line *dwarfx.LineEntry) {
	if m.functab == nil || m.linetab == nil {
		return "", nil
	}

	i := sort.Search(len(m.functab), func(i int) bool {
		return ip < m.functab[i].highpc
	})
	if i < len(m.functab) && m.functab[i].lowpc <= ip && ip < m.functab[i].highpc {
		fn = m.functab[i].name
	}

	i = sort.Search(len(m.linetab), func(i int) bool {
		return ip < m.linetab[i].Address
	})
	if i != 0 {
		line = m.linetab[i-1]
	}

	return
}

func getMmapExtra(mmap *perfsession.Mmap) *mmapExtra {
	if mmap.Extra != nil {
		return mmap.Extra.(*mmapExtra)
	}

	// Load ELF
	elff, err := elf.Open(mmap.Filename)
	if err != nil {
		return nil
	}
	defer elff.Close()

	// Load DWARF
	dwarff, err := elff.DWARF()
	if err != nil {
		return nil
	}

	extra := &mmapExtra{
		dwarfFuncTable(dwarff),
		dwarfLineTable(elff, dwarff),
	}
	mmap.Extra = extra
	return extra
}

func limitFuncs(stats []*lineStat, limit int) []*lineStat {
	seen := 0
	for i, stat := range stats {
		if i == 0 || stat.fn != stats[i-1].fn {
			if seen == limit {
				return stats[:i]
			}
			seen++
		}
	}
	return stats
}

func sections(count int, newGroup func(int) bool) [][2]int {
	sections := make([][2]int, 0)
	if count == 0 {
		return sections
	}

	start := 0
	for i := 1; i < count; i++ {
		if newGroup(i) {
			sections = append(sections, [2]int{start, i})
			start = i
		}
	}
	sections = append(sections, [2]int{start, count})
	return sections
}

func getLine(path string, line int) string {
	// TODO: Cache parsing

	file, err := os.Open(path)
	if err != nil {
		log.Println(err)
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for i := 0; i < line && scanner.Scan(); i++ {
		// Do nothing
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
	return scanner.Text()
}
