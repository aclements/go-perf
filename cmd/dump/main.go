// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"

	"github.com/aclements/go-perf/perffile"
)

func main() {
	var (
		flagInput = flag.String("i", "perf.data", "input perf.data `file`")
		flagOrder = flag.String("order", "time", "sort `order`; one of: file, time, causal")
	)
	flag.Parse()
	order, ok := parseOrder(*flagOrder)
	if flag.NArg() > 0 || !ok {
		flag.Usage()
		os.Exit(1)
	}

	f, err := perffile.Open(*flagInput)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	fmt.Printf("%+v\n", f)

	if buildIDs, err := f.BuildIDs(); err != nil {
		log.Fatal(err)
	} else if buildIDs != nil {
		fmt.Printf("build IDs:\n")
		for _, bid := range buildIDs {
			fmt.Printf("  %v\n", bid)
		}
	}

	nrCPUs := func() ([]int, error) {
		online, avail, err := f.NrCPUs()
		if online == 0 && avail == 0 {
			return nil, err
		}
		return []int{online, avail}, err
	}
	cpuTopology := func() ([][]perffile.CPUSet, error) {
		cores, threads, err := f.CPUTopology()
		if cores == nil {
			return nil, err
		}
		return [][]perffile.CPUSet{cores, threads}, err
	}

	for _, hdr := range []struct {
		label string
		fetch interface{}
	}{
		//{"build IDs", f.BuildIDs},
		{"hostname", f.Hostname},
		{"OS release", f.OSRelease},
		{"version", f.Version},
		{"arch", f.Arch},
		{"nrcpus", nrCPUs},
		{"CPU desc", f.CPUDesc},
		{"CPUID", f.CPUID},
		{"total memory", f.TotalMem},
		{"cmdline", f.CmdLine},
		{"CPU topology", cpuTopology},
		{"NUMA topology", f.NUMATopology},
		{"PMU mappings", f.PMUMappings},
		{"groups", f.GroupDesc},
	} {
		res := reflect.ValueOf(hdr.fetch).Call(nil)
		if !res[1].IsNil() {
			log.Fatal(res[1].Interface())
		}
		if res[0].Interface() != reflect.Zero(res[0].Type()) {
			fmt.Printf("%s: %v\n", hdr.label, res[0].Interface())
		}
	}

	rs := f.Records(order)
	for rs.Next() {
		fmt.Printf("%v %+v\n", rs.Record.Type(), rs.Record)
	}
	if err := rs.Err(); err != nil {
		log.Fatal(err)
	}
}

func parseOrder(order string) (perffile.RecordsOrder, bool) {
	switch order {
	case "file":
		return perffile.RecordsFileOrder, true
	case "time":
		return perffile.RecordsTimeOrder, true
	case "causal":
		return perffile.RecordsCausalOrder, true
	}
	return 0, false
}
