// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
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
)

func main() {
	var (
		flagInput   = flag.String("i", "perf.data", "read memory latency profile from `file`")
		flagHttp    = flag.String("http", ":8001", "serve HTTP on `address`")
		flagDocRoot = flag.String("docroot", "./static", "alternate `path` to static web resources")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(1)
	}

	db := parsePerf(*flagInput)
	fmt.Fprintln(os.Stderr, "profile loaded")

	mux := http.NewServeMux()
	// TODO: Don't assume static/ is in the working directory.
	// Look at godoc's makestatic.go and -template argument.
	mux.Handle("/", http.FileServer(http.Dir(*flagDocRoot)))
	mux.Handle("/h", &heatMapHandler{db})
	mux.Handle("/metadata", &metadataHandler{*flagInput, db.metadata})

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

	case "line":
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
	}

	h.db.filter(&f, agg)

	// Sort histograms by weight.
	sort.Sort(sort.Reverse(weightSorter(histograms)))

	if limit != 0 && limit < len(histograms) {
		histograms = histograms[:limit]
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

type latencyHistogram struct {
	scale  scale.Quantitative
	Bins   []int
	weight int

	PID      int    `json:"pid,omitempty"`
	Comm     string `json:"comm,omitempty"`
	FuncName string `json:"funcName,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Line     int    `json:"line,omitempty"`
	Address  uint64 `json:"address,omitempty"`
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
	return w[i].weight < w[j].weight
}

func (w weightSorter) Swap(i, j int) {
	w[i], w[j] = w[j], w[i]
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
