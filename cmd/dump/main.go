// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"

	"github.com/aclements/goperf/perffile"
)

func main() {
	f, err := perffile.Open("perf.data")
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

	rs := f.Records()
	for rs.Next() {
		fmt.Printf("%v %+v\n", rs.Record.Type(), rs.Record)
	}
	if err := rs.Err(); err != nil {
		log.Fatal(err)
	}
}
