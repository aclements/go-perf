// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command memlat is a web-based browser for memory load latency profiles.
//
// Memory stalls and conflicts are increasingly important for software
// performance. Memory load latency profiles can give deep insights in
// to these problems; however, the richness of these profiles makes
// them difficult to interpret using traditional profiling tools and
// techniques.
//
// memlat is a profile browser built for understanding and
// interpreting memory load latency profiles. The central concept is a
// "latency distribution", which is a statistical distribution of the
// number of cycles spent in memory load or store operations. For
// example, if there are 10 loads that take 10 cycles and 2 loads that
// takes 100 cycles, the latency distribution consists of a 100 cycle
// spike at 10 cycles and a 200 cycle spike at 100 cycles. The total
// weight of this distribution accounts for the total cycles spent
// waiting on memory loads or stores.
//
// memlat presents a profile as a multidimensional latency
// distribution and provides a tools for viewing and filtering this
// distribution on each dimension, such as by function, by source
// line, by data source (L1 hit, TLB miss, etc), by address, etc. Each
// tab in the UI browses the profile on a different dimension and
// clicking on a row filters the profile down to just that function,
// source line, etc. An active filters can be removed by clicking on
// it in the filter bar at the top.
//
// For example, suppose we want to understand the primary source of
// memory latency in a profile. Select the "By source line" tab and
// click on the top row to filter to the source line that contributed
// the most total memory latency. You can select the "Source
// annotation" tab to see the text of this line. To drill down to a
// particular memory address, select the "By address" tab to see the
// memory addresses touched by this source line. Click the top one to
// further filter to the hottest address touched by this source line.
// Then, click the source line filter in the filter bar at the top to
// remove the source line filter. Finally, select the "Source
// annotation" tab to see the other source code lines that touch this
// hot address.
//
// Note that the latency reported by the hardware is the time from
// instruction issue to retire. Hence, a "fast load" (say, an L1 hit)
// that happens immediately after a "slow load" (say, an LLC miss),
// will have a high reported latency because it has to wait for the
// slow load, even though the actual memory operation for the fast
// load is fast.
//
// Usage
//
// To download and install memlat, run
//
//    go get github.com/aclements/go-perf/cmd/memlat
//
// memlat works with the memory load latency profiles recorded by the
// Linux perf tool. This requires hardware support that has been
// available since Intel Nehalem. To record a memory latency profile,
// use perf's "mem" subcommand. For example,
//
//    perf mem record <command>  # Record a memory profile for command
//    perf mem record -a         # Record a system-wide memory profile
//
// This will write the profile to a file called perf.data. Then,
// simply start memlat with
//
//    memlat
//
// memlat will parse and symbolize the profile and start a web server
// listening by default on localhost:8001.
package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"

	"github.com/aclements/go-moremath/scale"
	"github.com/aclements/go-moremath/vec"
	"github.com/aclements/go-perf/perffile"
)

//go:embed static
var staticFiles embed.FS

// TODO: Open a browser automatically. Maybe bind to any address in
// this mode.

// TODO: Does this correctly handle the dynamic sampling rate that
// perf mem record uses by default?

func main() {
	var (
		flagInput   = flag.String("i", "perf.data", "read memory latency profile from `file`")
		flagHttp    = flag.String("http", "localhost:8001", "serve HTTP on `address`")
		flagDocRoot = flag.String("docroot", "", "alternate `path` to static web resources")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "loading profile...")
	db := parsePerf(*flagInput)
	fmt.Fprintln(os.Stderr, "profile loaded")

	mux := http.NewServeMux()
	if *flagDocRoot == "" {
		// Use the embedded static assets.
		sub, _ := fs.Sub(staticFiles, "static")
		mux.Handle("/", http.FileServer(http.FS(sub)))
	} else {
		// Use assets from the file system.
		mux.Handle("/", http.FileServer(http.Dir(*flagDocRoot)))
	}
	mux.Handle("/h", &heatMapHandler{db})
	mux.Handle("/metadata", &metadataHandler{*flagInput, db.metadata})

	fmt.Fprintf(os.Stderr, "serving on %s\n", *flagHttp)
	if err := http.ListenAndServe(*flagHttp, mux); err != nil {
		log.Fatal(err)
	}
}

type heatMapHandler struct {
	db *database
}

func (h *heatMapHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// TOOD: Include a signature for this profile in the request
	// and mark the response as cacheable.

	// TODO: Compress the output.

	// Request includes filter, group by. Response: map from group
	// by to histograms.
	qs, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	atoi := func(s string) int {
		x, _ := strconv.Atoi(s)
		return x
	}
	f := filter{
		pid:      atoi(qs.Get("pid")),
		funcName: qs.Get("funcName"),
		fileName: qs.Get("fileName"),
		line:     atoi(qs.Get("line")),
		address:  uint64(atoi(qs.Get("address"))),
		dataSrc: perffile.DataSrc{
			Op:     perffile.DataSrcOp(atoi(qs.Get("op"))),
			Miss:   qs.Get("miss") == "miss",
			Level:  perffile.DataSrcLevel(atoi(qs.Get("level"))),
			Snoop:  perffile.DataSrcSnoop(atoi(qs.Get("snoop"))),
			Locked: perffile.DataSrcLock(atoi(qs.Get("locked"))),
			TLB:    perffile.DataSrcTLB(atoi(qs.Get("tlb"))),
		},
	}
	groupBy := qs.Get("groupBy")
	limit := atoi(qs.Get("limit"))

	// Compute the scale for this histogram set.
	const useLocalScale = false
	var maxLatency uint32 = 1
	if useLocalScale {
		h.db.filter(&f, func(p *proc, rec *record) {
			if rec.latency > maxLatency {
				maxLatency = rec.latency
			}
		})
	} else {
		maxLatency = h.db.maxLatency
	}
	scaler, err := scale.NewLog(1, float64(maxLatency), 10)
	if err != nil {
		log.Fatal(err)
	}
	scaler.Nice(scale.TickOptions{Max: 6})

	var histograms []*latencyHistogram
	newHist := func() *latencyHistogram {
		hist := newLatencyHistogram(&scaler)
		histograms = append(histograms, hist)
		return hist
	}

	// Create aggregation function.
	var agg func(*proc, *record)
	switch groupBy {
	default:
		http.Error(w, fmt.Sprintf("unknown groupby %q", groupBy), http.StatusBadRequest)
		return

	case "all":
		hist := newHist()
		agg = func(p *proc, r *record) {
			hist.update(r)

		}

	case "pid":
		groups := make(map[*proc]*latencyHistogram)
		agg = func(p *proc, r *record) {
			hist, ok := groups[p]
			if !ok {
				hist = newHist()
				hist.PID = p.pid
				hist.Comm = p.comm
				groups[p] = hist
			}
			hist.update(r)
		}

	case "funcName":
		groups := make(map[string]*latencyHistogram)
		agg = func(p *proc, r *record) {
			funcName := p.ipInfo[r.ip].funcName
			hist, ok := groups[funcName]
			if !ok {
				hist = newHist()
				hist.FuncName = funcName
				groups[funcName] = hist
			}
			hist.update(r)
		}

	case "annotation", "line":
		groups := make(map[ipInfo]*latencyHistogram)
		agg = func(p *proc, r *record) {
			ipInfo := p.ipInfo[r.ip]
			hist, ok := groups[ipInfo]
			if !ok {
				hist = newHist()
				hist.FileName = ipInfo.fileName
				hist.Line = ipInfo.line
				groups[ipInfo] = hist
			}
			hist.update(r)
		}

	case "address":
		groups := make(map[uint64]*latencyHistogram)
		agg = func(p *proc, r *record) {
			hist, ok := groups[r.address]
			if !ok {
				hist = newHist()
				hist.Address = r.address
				groups[r.address] = hist
			}
			hist.update(r)
		}

	case "dataSrc":
		groups := make(map[perffile.DataSrc]*latencyHistogram)
		add1 := func(r *record, key perffile.DataSrc, label, group string) {
			hist, ok := groups[key]
			if !ok {
				hist = newHist()
				hist.group = group
				hist.Op = key.Op
				hist.Miss = key.Miss
				hist.Level = key.Level
				hist.Snoop = key.Snoop
				hist.Locked = key.Locked
				hist.TLB = key.TLB
				hist.DataSrcLabel = label
				groups[key] = hist
			}
			hist.update(r)
		}
		agg = func(p *proc, r *record) {
			ds := h.db.dataSrcs[r.dataSrc]
			if ds.Op != 0 {
				add1(r, perffile.DataSrc{Op: ds.Op}, ds.Op.String(), "Operation")
			}
			if ds.Level != 0 {
				miss := " hit"
				if ds.Miss {
					miss = " miss"
				}
				add1(r, perffile.DataSrc{Miss: ds.Miss, Level: ds.Level}, ds.Level.String()+miss, "Cache level")
			}
			if ds.Snoop != 0 {
				add1(r, perffile.DataSrc{Snoop: ds.Snoop}, ds.Snoop.String(), "Snoop")
			}
			if ds.Locked != 0 {
				add1(r, perffile.DataSrc{Locked: ds.Locked}, ds.Locked.String()[11:], "Locked")
			}
			if ds.TLB != 0 {
				add1(r, perffile.DataSrc{TLB: ds.TLB}, ds.TLB.String(), "TLB")
			}
		}
	}

	h.db.filter(&f, agg)

	// Sort histograms by weight.
	sort.Sort(sort.Reverse(weightSorter(histograms)))

	// Take the top N histograms.
	if groupBy == "dataSrc" {
		limit = 0
	}
	if limit != 0 && limit < len(histograms) {
		histograms = histograms[:limit]
	}

	// Special processing for some grouping types.
	switch groupBy {
	case "dataSrc":
		// Add group headers
		for i := 0; i < len(histograms); i++ {
			if i == 0 || histograms[i-1].group != histograms[i].group {
				histograms = append(histograms, nil)
				copy(histograms[i+1:], histograms[i:])
				histograms[i] = &latencyHistogram{
					Text:     histograms[i+1].group,
					IsHeader: true,
				}
				i++
			}
		}

	case "annotation":
		if len(histograms) == 0 {
			break
		}
		// TODO: When loading profile, check for out-of-date
		// source files and warn.

		ranges := []sourceRange{}
		histMap := map[ipInfo]*latencyHistogram{}
		for _, hist := range histograms {
			ranges = append(ranges, sourceRange{hist.FileName, hist.Line, hist.Line + 1, hist.weight})
			histMap[ipInfo{"", hist.FileName, hist.Line}] = hist
		}
		ranges = expandSourceRanges(ranges, 5)

		// Sort ranges by max weight histogram.
		sort.Slice(ranges, func(i, j int) bool {
			return ranges[i].maxWeight > ranges[j].maxWeight
		})

		// Collect lines from ranges.
		histograms = []*latencyHistogram{}
		for _, r := range ranges {
			lines, r, err := getLines(r)
			if err != nil {
				log.Println(err)
				continue
			}
			if len(lines) == 0 {
				continue
			}

			// Add a header for this range of source.
			header := newLatencyHistogram(&scaler)
			header.Bins = nil
			header.Text = r.file
			header.IsHeader = true
			histograms = append(histograms, header)

			for i, text := range lines {
				hist := histMap[ipInfo{"", r.file, r.start + i}]
				if hist == nil {
					hist = newLatencyHistogram(&scaler)
					hist.FileName = r.file
					hist.Line = r.start + i
					hist.Bins = nil
				}
				hist.Text = text
				histograms = append(histograms, hist)
			}
		}
	}

	// Compute maximum bin size for bin scaling.
	maxBin := 0
	for _, hist := range histograms {
		max := hist.max()
		if max > maxBin {
			maxBin = max
		}
	}

	// Construct JSON reply.
	major, minor := scaler.Ticks(scale.TickOptions{Max: 6})
	majorX, minorX := vec.Map(scaler.Map, major), vec.Map(scaler.Map, minor)
	err = json.NewEncoder(w).Encode(struct {
		Histograms []*latencyHistogram
		MaxBin     int

		MajorTicks, MajorTicksX []float64
		MinorTicksX             []float64
	}{histograms, maxBin, major, majorX, minorX})
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

const latencyHistogramBins = 60

// TODO: There's a lot of code for transforming between filters in the
// query string, latencyHistogram, and the filter struct. Unify these
// better.

type latencyHistogram struct {
	scale  scale.Quantitative
	Bins   []int `json:",omitempty"`
	weight int
	group  string

	// Filter specification.
	PID      int    `json:"pid,omitempty"`
	Comm     string `json:"comm,omitempty"`
	FuncName string `json:"funcName,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Line     int    `json:"line,omitempty"`
	Address  uint64 `json:"address,omitempty"`

	// Data source filter specification.
	Op     perffile.DataSrcOp    `json:"op,omitempty"`
	Miss   bool                  `json:"miss,omitempty"`
	Level  perffile.DataSrcLevel `json:"level,omitempty"`
	Snoop  perffile.DataSrcSnoop `json:"snoop,omitempty"`
	Locked perffile.DataSrcLock  `json:"locked,omitempty"`
	TLB    perffile.DataSrcTLB   `json:"tlb,omitempty"`

	// Presentation.
	Text         string `json:"text,omitempty"`
	DataSrcLabel string `json:"dataSrcLabel,omitempty"`
	IsHeader     bool   `json:"isHeader,omitempty"`
}

func newLatencyHistogram(scale scale.Quantitative) *latencyHistogram {
	return &latencyHistogram{
		scale:  scale,
		Bins:   make([]int, latencyHistogramBins),
		weight: 0,
	}
}

func (h *latencyHistogram) update(r *record) {
	bin := int(h.scale.Map(float64(r.latency)) * latencyHistogramBins)
	if bin < 0 {
		bin = 0
	}
	if bin >= latencyHistogramBins {
		bin = latencyHistogramBins
	}
	h.Bins[bin] += int(r.latency)
	h.weight += int(r.latency)
}

func (h *latencyHistogram) max() int {
	out := 0
	for _, count := range h.Bins {
		if count > out {
			out = count
		}
	}
	return out
}

type weightSorter []*latencyHistogram

func (w weightSorter) Len() int {
	return len(w)
}

func (w weightSorter) Less(i, j int) bool {
	if w[i].group != w[j].group {
		return w[i].group > w[j].group
	}
	return w[i].weight < w[j].weight
}

func (w weightSorter) Swap(i, j int) {
	w[i], w[j] = w[j], w[i]
}

type sourceRange struct {
	file       string
	start, end int
	maxWeight  int
}

func expandSourceRanges(r []sourceRange, by int) []sourceRange {
	sort.Slice(r, func(i, j int) bool {
		if r[i].file != r[j].file {
			return r[i].file < r[j].file
		}
		return r[i].start < r[j].start
	})

	// Expand ranges.
	for i := range r {
		r[i].start -= by
		r[i].end += by
	}

	// Merge ranges.
	i := 0
	for j := 1; j < len(r); j++ {
		if r[i].file == r[j].file && r[i].end >= r[j].start {
			if r[j].end > r[i].end {
				r[i].end = r[j].end
			}
			if r[j].maxWeight > r[i].maxWeight {
				r[i].maxWeight = r[j].maxWeight
			}
		} else {
			i++
			r[i] = r[j]
		}
	}
	return r[:i+1]
}

func getLines(r sourceRange) ([]string, sourceRange, error) {
	lines := []string{}

	file, err := os.Open(r.file)
	if err != nil {
		return nil, r, err
	}
	defer file.Close()

	// Skip to start.
	if r.start < 0 {
		r.start = 0
	}
	scanner := bufio.NewScanner(file)
	for i := 0; i < r.start && scanner.Scan(); i++ {
		// Do nothing
	}

	for i := 0; i < r.end-r.start && scanner.Err() == nil; i++ {
		lines = append(lines, scanner.Text())
		scanner.Scan()
	}
	if err := scanner.Err(); err != nil {
		return nil, r, err
	}

	return lines, r, nil
}

type metadataHandler struct {
	Filename string
	Metadata
}

func (h *metadataHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	err := json.NewEncoder(w).Encode(h)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
