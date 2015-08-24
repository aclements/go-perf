// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aclements/goperf/perffile"
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

	if hostname, err := f.Hostname(); err != nil {
		log.Fatal(err)
	} else if hostname != "" {
		fmt.Printf("hostname: %s\n", hostname)
	}

	if cmdline, err := f.CmdLine(); err != nil {
		log.Fatal(err)
	} else if cmdline != nil {
		fmt.Printf("cmdline: %v\n", cmdline)
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
