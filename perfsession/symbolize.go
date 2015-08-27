// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perfsession

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"io"
	"log"
	"os/user"
	"sort"
)

type Symbolic struct {
	FuncName string
	Line     dwarf.LineEntry
}

func Symbolize(session *Session, mmap *Mmap, ip uint64, out *Symbolic) bool {
	s := getSymbolicExtra(session, mmap.Filename)
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

var buildIDDir = (func() string {
	// See set_buildid_dir in tools/perf/util/config.c.
	u, err := user.Current()
	if err != nil {
		return ".debug"
	}
	return fmt.Sprintf("%s/.debug", u.HomeDir)
})()

func getSymbolicExtra(session *Session, filename string) *symbolicExtra {
	tables, ok := session.Extra[symbolicExtraKey].(map[string]*symbolicExtra)
	if !ok {
		tables = make(map[string]*symbolicExtra)
		session.Extra[symbolicExtraKey] = tables
	}

	extra, ok := tables[filename]
	if ok {
		return extra
	}
	tables[filename] = (*symbolicExtra)(nil)

	// See dso__data_fd in toosl/perf/util/dso.c.

	// TODO: Handle kernel symbols. See dso__find_kallsyms.

	// Try build ID cache first.
	//
	// TODO: Cache filename to build ID mapping.
	bids, err := session.File.BuildIDs()
	if err != nil {
		log.Fatal(err)
	}
	for _, bid := range bids {
		if bid.Filename == filename {
			nfilename := fmt.Sprintf("%s/.build-id/%.2s/%s", buildIDDir, bid.BuildID, bid.BuildID.String()[2:])
			extra, err = newSymbolicExtra(nfilename)
			if err == nil {
				break
			}
		}
	}

	// Try original path.
	if extra == nil {
		extra, err = newSymbolicExtra(filename)
		if err != nil {
			log.Println(err)
		}
	}

	tables[filename] = extra
	return extra
}

func newSymbolicExtra(filename string) (*symbolicExtra, error) {
	// Load ELF
	//
	// TODO: Relocate ELF.
	elff, err := elf.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("error loading ELF file %s: %s", filename, err)
	}
	defer elff.Close()

	// Load DWARF
	//
	// TODO: Support build IDs and split DWARF
	if elff.Section(".debug_info") == nil {
		return nil, fmt.Errorf("no DWARF info for %s", filename)
	}
	dwarff, err := elff.DWARF()
	if err != nil {
		return nil, fmt.Errorf("error loading DWARF from %s: %s", filename, err)
	}

	return &symbolicExtra{
		dwarfFuncTable(dwarff),
		dwarfLineTable(dwarff),
	}, nil
}

type symbolicExtra struct {
	functab []funcRange
	linetab []dwarf.LineEntry
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
	// TODO Use ELF symbol table if DWARF isn't available.

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
