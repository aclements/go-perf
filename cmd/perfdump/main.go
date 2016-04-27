// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command perfdump prints the raw contents of a perf.data profile.
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

	fmt.Printf("events:\n")
	for _, event := range f.Events {
		fmt.Printf("  %p=%+v\n", event, *event)
	}

	if f.Meta.BuildIDs != nil {
		fmt.Printf("build IDs:\n")
		for _, bid := range f.Meta.BuildIDs {
			fmt.Printf("  %v\n", bid)
		}
	}

	for _, hdr := range []struct {
		label string
		val   interface{}
	}{
		//{"build IDs", &f.Meta.BuildIDs},
		{"hostname", f.Meta.Hostname},
		{"OS release", f.Meta.OSRelease},
		{"version", f.Meta.Version},
		{"arch", f.Meta.Arch},
		{"CPUs online", f.Meta.CPUsOnline},
		{"CPUs available", f.Meta.CPUsAvail},
		{"CPU desc", f.Meta.CPUDesc},
		{"CPUID", f.Meta.CPUID},
		{"total memory", f.Meta.TotalMem},
		{"cmdline", f.Meta.CmdLine},
		{"core groups", f.Meta.CoreGroups},
		{"thread groups", f.Meta.ThreadGroups},
		{"NUMA nodes", f.Meta.NUMANodes},
		{"PMU mappings", f.Meta.PMUMappings},
		{"groups", f.Meta.Groups},
	} {
		if hdr.val == reflect.Zero(reflect.ValueOf(hdr.val).Type()) {
			continue
		}
		fmt.Printf("%s: %v\n", hdr.label, hdr.val)
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
