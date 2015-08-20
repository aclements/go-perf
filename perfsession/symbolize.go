// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perfsession

import (
	"debug/dwarf"
	"debug/elf"
	"io"
	"log"
	"sort"
)

type Symbolic struct {
	FuncName string
	Line     dwarf.LineEntry
}

func Symbolize(mmap *Mmap, ip uint64, out *Symbolic) bool {
	s := getSymbolicExtra(mmap)
	if s == nil {
		return false
	}
	f, l := s.findIP(ip)
	if f == nil {
		out.FuncName = ""
	} else {
		out.FuncName = f.name
	}
	if l == nil {
		out.Line = dwarf.LineEntry{}
	} else {
		out.Line = *l
	}
	return true
}

var symbolicExtraKey = NewExtraKey("perfsession.symbolicExtra")

func getSymbolicExtra(mmap *Mmap) *symbolicExtra {
	extra, ok := mmap.Extra[symbolicExtraKey].(*symbolicExtra)
	if ok {
		return extra
	}

	// Load ELF
	//
	// TODO: Relocate ELF.
	elff, err := elf.Open(mmap.Filename)
	if err != nil {
		log.Printf("error loading ELF file %s: %s\n", mmap.Filename, err)
		mmap.Extra[symbolicExtraKey] = (*symbolicExtra)(nil)
		return nil
	}
	defer elff.Close()

	// Load DWARF
	//
	// TODO: Support build IDs and split DWARF
	dwarff, err := elff.DWARF()
	if err != nil {
		log.Printf("error loading DWARF from %s: %s\n", mmap.Filename, err)
		mmap.Extra[symbolicExtraKey] = (*symbolicExtra)(nil)
		return nil
	}

	extra = &symbolicExtra{
		dwarfFuncTable(dwarff),
		dwarfLineTable(dwarff),
	}
	mmap.Extra[symbolicExtraKey] = extra
	return extra
}

type symbolicExtra struct {
	functab []funcRange
	linetab []dwarf.LineEntry
}

func (s *symbolicExtra) Fork(pid int) Forkable {
	return s
}

func (s *symbolicExtra) findIP(ip uint64) (f *funcRange, l *dwarf.LineEntry) {
	if s.functab == nil || s.linetab == nil {
		return nil, nil
	}

	i := sort.Search(len(s.functab), func(i int) bool {
		return ip < s.functab[i].highpc
	})
	if i < len(s.functab) && s.functab[i].lowpc <= ip && ip < s.functab[i].highpc {
		f = &s.functab[i]
	}

	i = sort.Search(len(s.linetab), func(i int) bool {
		return ip < s.linetab[i].Address
	})
	if i != 0 && !s.linetab[i-1].EndSequence {
		l = &s.linetab[i-1]
	}

	return
}

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
		// apparently gc doesn't produce these.
		//
		// TODO: Support DW_AT_ranges.
	tag:
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
			var highpc uint64
			switch highpcx := ent.Val(dwarf.AttrHighpc).(type) {
			case uint64:
				highpc = highpcx
			case int64:
				highpc = lowpc + uint64(highpcx)
			default:
				break tag
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

func dwarfLineTable(dwarff *dwarf.Data) []dwarf.LineEntry {
	out := make([]dwarf.LineEntry, 0)

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
			out = append(out, lent)
		}
	}
	return out
}
