// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command memanim creates an animation of memory accesses over time
// from a "perf mem record" profile. In the animation, the address
// space is compacted to remove pages that have no recorded references
// and then mapped on to Hilbert curve so that nearby accesses appear
// nearby in 2-D space. It is then broken in to panels showing all
// accesses, L2-and-up accesses, etc.
//
// The simplest way to record a memory load profile is "perf mem
// record <cmd>".
//
// To record only load latency events over a threshold number of
// cycles, use the following command on Sandy Bridge or later:
//
//   perf record -W -d -e cpu/event=0xcd,umask=0x1,ldlat=<thresh>/pp <cmd>
//
// The minimum (and default) latency threshold is 3 cycles.
//
// At a reasonably high latency threshold, such as 50 cycles, it's
// possible to crank up to recording every single load with, e.g.,
// --count 1 -m 1024.
//
// To collect only user-space loads, change pp to ppu.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/ioutil"
	"log"
	"math"
	"os"
	"runtime/pprof"
	"sort"

	"github.com/aclements/go-perf/perffile"
	"github.com/golang/freetype"
)

const pageBytes = 4096

func main() {
	var (
		flagInput      = flag.String("i", "perf.data", "read memory latency profile from `file`")
		flagBy         = flag.String("by", "address", "`layout` by \"address\" or \"pc\"")
		flagFPS        = flag.Int("fps", 24, "frames per second")
		flagDilation   = flag.Float64("dilation", 1, "time dilation factor")
		flagWidth      = flag.Int("w", 512, "output width/height; must be a power of 2")
		flagCpuProfile = flag.String("cpuprofile", "", "write cpu profile to file")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(1)
	}

	if *flagWidth <= 0 || *flagWidth&(*flagWidth-1) != 0 {
		fmt.Fprintln(os.Stderr, "width must be a power of two")
		os.Exit(1)
	}

	if !(*flagBy == "address" || *flagBy == "pc") {
		fmt.Fprintln(os.Stderr, "-by must be address or pc")
		os.Exit(1)
	}

	if *flagCpuProfile != "" {
		f, err := os.Create(*flagCpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	// TODO: Do something better with weight? I tried blue/red
	// coloring, but it really isn't obvious. Fade it out at a
	// certain rate? Shade? Ripples?

	events := parsePerf(*flagInput, *flagBy)

	// Canonicalize the events.
	imgSize := *flagWidth
	mapper := newAddrMapper(events, uint64(imgSize*imgSize-1))
	normalizeWeight(events)
	zeroTime(events)
	lastTime := events[len(events)-1].time

	// Load font.
	//
	// TODO Don't hard-code it's location. Unfortunately, there's
	// no fontconfig equivalent for Go that I can find.
	fontCtx := freetype.NewContext()
	fontData, err := ioutil.ReadFile("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf")
	if err != nil {
		log.Fatal(err)
	}
	font, err := freetype.ParseFont(fontData)
	if err != nil {
		log.Fatal(err)
	}
	fontCtx.SetFontSize(12)
	fontCtx.SetSrc(image.Black)
	fontCtx.SetFont(font)
	fontBounds := font.Bounds(fontCtx.PointToFixed(12))
	labelHeight := int((fontBounds.Max.Y - fontBounds.Min.Y) >> 6)

	// Create image.
	img := image.NewNRGBA(image.Rect(0, 0, (imgSize+1)*numPanels-1, imgSize+labelHeight))
	draw.Draw(img, img.Bounds(), image.White, image.ZP, draw.Over)
	fontCtx.SetDst(img)
	fontCtx.SetClip(img.Bounds())

	// Construct sub-images for panels and draw framing elements.
	levelImgs := make([]*image.NRGBA, numPanels)
	for i := range levelImgs {
		left := (imgSize + 1) * i
		levelImgs[i] = img.SubImage(image.Rect(left, labelHeight, left+imgSize, imgSize+labelHeight)).(*image.NRGBA)
		levelImgs[i].Rect = levelImgs[i].Rect.Sub(levelImgs[i].Rect.Min)

		if i > 0 {
			for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
				img.Set(left-1, y, color.Black)
			}
		}
		fontCtx.DrawString(">= "+panelLevels[i].String(), freetype.Pt(left+2, 12))
	}

	// Create address space reference image.
	//
	// TODO: This isn't very useful, it turns out. I could simply
	// print out the map from coordinate to address. Visually, I
	// could underlay boundaries between very distinct parts of
	// the address space (say, find points that have a >1GB break
	// and mark the boundary between all pixels before that break
	// and after that break).
	if false {
		addrStep := int(math.Floor(1 / mapper.normFactor))
		for pfn := range mapper.pageBase {
			for offset := 0; offset < pageBytes; offset += addrStep {
				addr := pfn*pageBytes + uint64(offset)
				x, y := hilbert(imgSize, int(mapper.mapAddr(addr)))
				naddr := float64(addr%(1<<48)) / (1 << 48) * 2 * math.Pi
				cb, cr := math.Cos(naddr), -math.Sin(naddr)
				//fmt.Println(fmt.Sprintf("%016x", addr), int(mapper.mapAddr(addr)), naddr, cb, cr)
				r, g, b := color.YCbCrToRGB(127, uint8((cb+1)*127), uint8((cr+1)*127))
				img.SetNRGBA(x, y, color.NRGBA{r, g, b, 255})
			}
		}
		writePNG("addr.png", img)
	}

	nsPerFrame := int(1000000000 / (float64(*flagFPS) * *flagDilation))
	lastIndex := 0
	for frame := 0; ; frame++ {
		t0 := uint64(frame * nsPerFrame)
		t1 := uint64((frame + 1) * nsPerFrame)

		if t0 > lastTime {
			break
		}
		log.Println("frame", frame)

		// Fade the frame.
		//
		// TODO: The fade rate should be proportional to FPS.
		for _, levelImg := range levelImgs {
			for y := 0; y < levelImg.Rect.Dy(); y++ {
				scan := levelImg.Pix[y*levelImg.Stride : y*levelImg.Stride+levelImg.Rect.Dx()*4]
				for i, p := range scan {
					scan[i] = uint8(255 - (int(255-p) * 3 / 4))
				}
			}
		}

		// Draw the events.
		for evIndex, ev := range events[lastIndex:] {
			if ev.time < t0 {
				panic("time went backwards")
			}
			if t1 <= ev.time {
				lastIndex += evIndex
				break
			}

			addr := mapper.mapAddr(ev.addr)
			x, y := hilbert(imgSize, int(addr))
			//color := color.NRGBA{R: uint8(ev.weight), G: 0, B: 255 - uint8(ev.weight), A: 255}
			color := color.NRGBA{0, 0, 0, 255}
			for level := 0; level <= ev.level; level++ {
				levelImgs[level].SetNRGBA(x, y, color)
			}
		}

		// Write the frame out.
		writePNG(fmt.Sprintf("f%08d.png", frame), img)
	}

	fmt.Printf("%g bytes/pixel\n", 1/mapper.normFactor)
	fmt.Printf("%g pixels/page\n", mapper.normFactor*pageBytes)

	fmt.Printf("To combine frames:\n  mencoder 'mf://f*.png' -mf fps=%d -nosound -of lavf -lavfopts format=mp4 -ovc x264 -o out.mp4\n", *flagFPS)
}

type event struct {
	time   uint64
	addr   uint64
	weight uint64
	level  int
}

// parsePerf parses a perf.data profile and returns the cache miss
// events.
func parsePerf(fileName, by string) []event {
	f, err := perffile.Open(fileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading profile: %s\n", err)
		os.Exit(1)
	}
	defer f.Close()

	byPC := by == "pc"

	const requiredFormat = perffile.SampleFormatTime | perffile.SampleFormatAddr | perffile.SampleFormatWeight | perffile.SampleFormatDataSrc

	events := make([]event, 0)
	rs := f.Records(perffile.RecordsTimeOrder)
	for rs.Next() {
		r := rs.Record
		switch r := r.(type) {
		case *perffile.RecordSample:
			if r.Format&requiredFormat != requiredFormat {
				break
			}
			level := r.DataSrc.Level
			if r.DataSrc.Miss {
				level <<= 1
			}
			addr := r.Addr
			if byPC {
				addr = r.IP
			}
			events = append(events, event{r.Time, addr, r.Weight, levelToPanel[level]})
		}
	}

	return events
}

type addrMapper struct {
	pageBase   map[uint64]uint64
	normMax    uint64
	normFactor float64 // pixels/byte
}

// newAddrMapper returns an addrMapper that maps addresses in events
// to a compacted space in the range [0, normMax].
func newAddrMapper(events []event, normMax uint64) *addrMapper {
	am := &addrMapper{normMax: normMax}

	// Find all distinct pages and max address.
	pages := make([]uint64, 0)
	pageSet := make(map[uint64]bool)
	maxAddr := uint64(0)
	for _, ev := range events {
		page := ev.addr / pageBytes
		if pageSet[page] {
			continue
		}
		pageSet[page] = true
		pages = append(pages, page)

		if ev.addr > maxAddr {
			maxAddr = ev.addr
		}
	}
	sort.Sort(uint64Slice(pages))

	// Map pages to a compact sequence.
	am.pageBase = make(map[uint64]uint64, len(pages))
	for i, page := range pages {
		am.pageBase[page] = uint64(i) * pageBytes
	}

	// Compute normalization factor.
	compactMax := am.pageBase[maxAddr/pageBytes] + maxAddr%pageBytes
	if compactMax <= normMax {
		am.normFactor = 1
	} else {
		am.normFactor = float64(normMax) / float64(compactMax)
	}

	return am
}

func (am *addrMapper) mapAddr(addr uint64) uint64 {
	compact := am.pageBase[addr/pageBytes] + addr%pageBytes
	norm := uint64(float64(compact) * am.normFactor)
	if norm > am.normMax {
		norm = am.normMax
	}
	return norm
}

func normalizeWeight(events []event) {
	// Find the maximum weight.
	maxW := uint64(0)
	for _, ev := range events {
		if ev.weight > maxW {
			maxW = ev.weight
		}
	}

	// TODO: Log scale?

	// Normalize [0, maxW] to [0, 255].
	factor := float64(255) / float64(maxW)
	for i, ev := range events {
		w := uint64(float64(ev.weight) * factor)
		if w > 255 {
			w = 255
		}
		events[i].weight = w
	}
}

func zeroTime(events []event) {
	if len(events) == 0 {
		return
	}
	t0 := events[0].time
	for i := range events {
		events[i].time -= t0
	}
}

type uint64Slice []uint64

func (s uint64Slice) Len() int {
	return len(s)
}

func (s uint64Slice) Less(i, j int) bool {
	return s[i] < s[j]
}

func (s uint64Slice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// hilbert converts a 1-D point d to a coordinate (x, y) in an n×n
// Hilbert space.
func hilbert(n, d int) (x, y int) {
	// Based on Wikipedia.
	rot := func(s, x, y, rx, ry int) (int, int) {
		if ry == 0 {
			if rx == 1 {
				x = s - 1 - x
				y = s - 1 - y
			}
			x, y = y, x
		}
		return x, y
	}
	for s := 1; s < n; s *= 2 {
		rx := 1 & (d / 2)
		ry := 1 & (d ^ rx)
		x, y = rot(s, x, y, rx, ry)
		x += s * rx
		y += s * ry
		d /= 4
	}
	return
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// panelLevels maps from panel number to data source level.
var panelLevels = [...]perffile.DataSrcLevel{
	perffile.DataSrcLevelL1,
	perffile.DataSrcLevelL2,
	perffile.DataSrcLevelL3,
	perffile.DataSrcLevelLocalRAM,
	perffile.DataSrcLevelRemoteRAM1,
}

const numPanels = len(panelLevels)

// levelToPanel maps from a data source level to a panel number.
var levelToPanel = map[perffile.DataSrcLevel]int{
	perffile.DataSrcLevelNA: 0,
}

func init() {
	for panel, level := range panelLevels {
		levelToPanel[level] = panel
	}

	var l int
	for i := perffile.DataSrcLevelL1; i <= perffile.DataSrcLevelUncached; i++ {
		if l2, ok := levelToPanel[i]; ok {
			l = l2
		} else {
			levelToPanel[i] = l
		}
	}
}
