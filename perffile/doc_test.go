// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"fmt"
	"log"
)

func Example() {
	f, err := Open("perf.data")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	rs := f.Records(RecordsTimeOrder)
	for rs.Next() {
		switch r := rs.Record.(type) {
		case *RecordSample:
			fmt.Printf("sample: %+v\n", r)
		}
	}
	if err := rs.Err(); err != nil {
		log.Fatal(err)
	}
}
