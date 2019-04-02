// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perfsession

import "sort"

// Ranges stores data associated with ranges of uint64 values and
// supports efficient lookup.
type Ranges struct {
	rs     []rangeEnt
	sorted bool
}

type rangeEnt struct {
	lo, hi uint64
	val    interface{}
}

// Add inserts val for range [lo, hi).
//
// Add is undefined if [lo, hi) overlaps a range already in r.
func (r *Ranges) Add(lo, hi uint64, val interface{}) {
	r.rs = append(r.rs, rangeEnt{lo, hi, val})
	r.sorted = false
}

// Get returns the range and the value for the range containing idx.
func (r *Ranges) Get(idx uint64) (lo, hi uint64, val interface{}, ok bool) {
	if r == nil {
		return 0, 0, nil, false
	}

	rs := r.rs
	if !r.sorted {
		sort.Slice(rs, func(i, j int) bool {
			return rs[i].lo < rs[j].lo
		})
		r.sorted = true
	}

	i := sort.Search(len(rs), func(i int) bool {
		return idx < rs[i].hi
	})
	if i < len(rs) && rs[i].lo <= idx && idx < rs[i].hi {
		return rs[i].lo, rs[i].hi, rs[i].val, true
	}
	return 0, 0, nil, false
}
