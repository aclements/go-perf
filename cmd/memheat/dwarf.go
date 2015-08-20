// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"debug/dwarf"
	"debug/elf"
	"io"
	"log"
	"sort"
)

type funcRange struct {
	name          string
	lowpc, highpc uint64
}

func dwarfFuncTable(dwarff *dwarf.Data) []funcRange {
	// Walk DWARF for functions
	// TODO: Use .debug_pubnames (not supported by dwarf package)
	r := dwarff.Reader()
	out := make([]funcRange, 0)
	for {
		ent, err := r.Next()
		if ent == nil || err != nil {
			break
		}
		// TODO: We should process TagInlinedSubroutine, but
		// apparently 6g doesn't produce these.
		switch ent.Tag {
		case dwarf.TagSubprogram:
			r.SkipChildren()
			name, ok := ent.Val(dwarf.AttrName).(string)
			if !ok {
				break
			}
			lowpc, ok := ent.Val(dwarf.AttrLowpc).(uint64)
			if !ok {
				break
			}
			highpc, ok := ent.Val(dwarf.AttrHighpc).(uint64)
			if !ok {
				break
			}
			out = append(out, funcRange{name, lowpc, highpc})

		case dwarf.TagCompileUnit, dwarf.TagModule, dwarf.TagNamespace:
			break

		default:
			r.SkipChildren()
		}
	}

	sort.Sort(funcRangeSorter(out))

	return out
}

type funcRangeSorter []funcRange

func (s funcRangeSorter) Len() int {
	return len(s)
}

func (s funcRangeSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s funcRangeSorter) Less(i, j int) bool {
	return s[i].lowpc < s[j].lowpc
}

func dwarfLineTable(elff *elf.File, dwarff *dwarf.Data) []*dwarf.LineEntry {
	out := make([]*dwarf.LineEntry, 0)

	// Iterate over compilation units
	dr := dwarff.Reader()
	for {
		ent, err := dr.Next()
		if ent == nil || err != nil {
			break
		}

		if ent.Tag != dwarf.TagCompileUnit {
			dr.SkipChildren()
			continue
		}

		// Decode CU's line table
		lr, err := dwarff.LineReader(ent)
		if err != nil {
			log.Fatal(err)
		} else if lr == nil {
			continue
		}

		for {
			var lent dwarf.LineEntry
			err := lr.Next(&lent)
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Fatal(err)
			}
			out = append(out, &lent)
		}
	}
	return out
}
