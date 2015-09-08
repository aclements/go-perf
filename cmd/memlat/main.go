// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command memlat is a web-based interactive browser for memory
// latency profiles.
//
// Intel CPUs starting with Nehalem support detailed hardware
// profiling of memory load operations and, in particular, load
// latency. Sandy Bridge introduced further support for profiling
// memory store operations. Memory stalls and conflicts are
// increasingly impacting software performance and these profiles can
// give incredible insight into these problems. However, the richness
// of these profiles make them difficult to interpret using
// traditional profiling tools and techniques.
//
// memlat is a profile browser built for understanding and
// interpreting memory latency profiles. The central concept is a
// latency distribution, which indicates how many cycles are spent in
// memory operations according to the latency of those operations. For
// example, if there are 10 loads that take 10 cycles and 2 loads that
// takes 100 cycles, the latency distribution consists of a 100 cycle
// spike at 10 cycles and a 200 cycle spike at 100 cycles. The total
// weight of this distribution accounts for the total cycles spent
// waiting on memory loads.
//
// To download and install memlat, run
//
//    go get github.com/aclements/go-perf/cmd/memlat
//
// memlat works with the standard memory latency profiles recorded by
// the Linux perf tool. To record a memory latency profile, use perf's
// "mem" subcommand. For example,
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
// listening by default on localhost;8001.
//
// TODO: Document the user interface once it's settled.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
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

//go:generate go run makestatic.go

// staticFiles is the static file tree baked in to the binary. This is
// assigned by an init function if the static files are available.
var staticFiles mapFS

// TODO: Open a browser automatically. Maybe bind to any address in
// this mode.

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
	if *flagDocRoot == "" && staticFiles == nil {
		// No baked-in static file system.
		*flagDocRoot = "static"
	}
	if *flagDocRoot == "" {
		mux.Handle("/", http.FileServer(staticFiles))
	} else {
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
	scale, err := scale.NewLog(1, float64(maxLatency), 10)
	if err != nil {
		log.Fatal(err)
	}
	scale.Nice(6)

	var histograms []*latencyHistogram
	newHist := func() *latencyHistogram {
		hist := newLatencyHistogram(&scale)
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
				if groupBy == "annotation" {
					// Stripped out below.
					hist.FuncName = ipInfo.funcName
				}
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

	switch groupBy {
	default:
		if limit != 0 && limit < len(histograms) {
			histograms = histograms[:limit]
		}

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

		// Find the top N functions.
		topFuncs := []string{}
		topFuncSet := map[string][]*latencyHistogram{}
		for _, hist := range histograms {
			if len(topFuncs) >= limit {
				break
			}
			if topFuncSet[hist.FuncName] == nil {
				topFuncs = append(topFuncs, hist.FuncName)
				topFuncSet[hist.FuncName] = []*latencyHistogram{}
			}
		}

		// Collect all histograms from the top functions.
		for _, hist := range histograms {
			if topFuncSet[hist.FuncName] != nil {
				topFuncSet[hist.FuncName] = append(topFuncSet[hist.FuncName], hist)
			}
			hist.FuncName = ""
		}
		histograms = []*latencyHistogram{}

		// Find the min and max line for each function.
		for funcName, set := range topFuncSet {
			minLine := set[0].Line
			maxLine := minLine
			haveLines := map[int]*latencyHistogram{}
			for _, h := range set {
				if h.Line < minLine {
					minLine = h.Line
				}
				if h.Line > maxLine {
					maxLine = h.Line
				}
				haveLines[h.Line] = h
			}

			// Read source lines and create empty histograms for
			// lines we missed.
			fileName := set[0].FileName
			lines, err := getLines(fileName, minLine, maxLine)
			if err != nil {
				log.Println(err)
				continue
			}
			if len(lines) == 0 {
				continue
			}
			for line := minLine; line <= maxLine; line++ {
				hist := haveLines[line]
				if hist == nil {
					hist = newLatencyHistogram(&scale)
					set = append(set, hist)
					hist.FileName = fileName
					hist.Line = line
					hist.Bins = nil
				}
				hist.Text = lines[line-minLine]
			}

			// Sort histograms by line number.
			sort.Sort(lineSorter(set))

			// Add function histograms to top-level list.
			header := newLatencyHistogram(&scale)
			header.Bins = nil
			header.Text = funcName
			header.IsHeader = true
			histograms = append(histograms, header)
			histograms = append(histograms, set...)
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
	major, minor := scale.Ticks(6)
	majorX, minorX := vec.Map(scale.Map, major), vec.Map(scale.Map, minor)
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

type lineSorter []*latencyHistogram

func (s lineSorter) Len() int {
	return len(s)
}

func (s lineSorter) Less(i, j int) bool {
	return s[i].Line < s[j].Line
}

func (s lineSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func getLines(path string, minLine, maxLine int) ([]string, error) {
	lines := make([]string, maxLine-minLine+1)

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Skip to minLine.
	scanner := bufio.NewScanner(file)
	for i := 0; i < minLine && scanner.Scan(); i++ {
		// Do nothing
	}

	for line := minLine; line <= maxLine && scanner.Err() == nil; line++ {
		lines[line-minLine] = scanner.Text()
		scanner.Scan()
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil

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
