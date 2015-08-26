// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A CPUSet represents a set of CPUs by CPU index.
type CPUSet []int

func parseCPUSet(str string) (CPUSet, error) {
	var err error
	out := CPUSet{}
	for _, r := range strings.Split(str, ",") {
		var lo, hi int
		dash := strings.Index(r, "-")
		if dash == -1 {
			lo, err = strconv.Atoi(r)
			if err != nil {
				return nil, err
			}
			hi = lo
		} else {
			lo, err = strconv.Atoi(r[:dash])
			if err != nil {
				return nil, err
			}
			hi, err = strconv.Atoi(r[dash+1:])
			if err != nil {
				return nil, err
			}
		}
		for cpu := lo; cpu <= hi; cpu++ {
			out = append(out, cpu)
		}
	}
	sort.Ints(out)
	i, j := 0, 0
	for ; i < len(out); i++ {
		if i != j && out[i] == out[j] {
			continue
		}
		out[j] = out[i]
		j++
	}
	return out, nil
}

func (c CPUSet) String() string {
	if len(c) == 0 {
		return ""
	}

	out := ""
	lo, hi := c[0], c[0]-1
	flush := func() {
		if lo == hi {
			out = fmt.Sprintf("%s,%d", out, lo)
		} else {
			out = fmt.Sprintf("%s,%d-%d", out, lo, hi)
		}
	}
	for _, cpu := range c {
		if cpu == hi+1 {
			hi = cpu
		} else {
			flush()
			lo, hi = cpu, cpu
		}
	}
	flush()
	return out[1:]
}
