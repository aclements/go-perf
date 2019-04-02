// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command prologuer reports what fraction of samples from a profile
// are from IPs marked as function prologue in DWARF line tables.
//
// In Go binaries (as of Go 1.12), this counts the stack bounds check
// up to and including the conditional branch to morestack. It does
// not the instructions that call morestack or the instructions that
// create a function's stack frame.
//
// This is best used with precise profile samples, such as
//
//   perf record -e cycles:ppp -- <cmd>
package main

import (
	"debug/dwarf"
	"debug/elf"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/aclements/go-perf/perffile"
	"github.com/aclements/go-perf/perfsession"
)

func main() {
	var flagInput = flag.String("i", "perf.data", "input perf.data `file`")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(1)
	}

	f, err := perffile.Open(*flagInput)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	s := perfsession.New(f)

	var samples, prologueSamples uint64
	rs := f.Records(perffile.RecordsFileOrder)
	for rs.Next() {
		s.Update(rs.Record)

		switch r := rs.Record.(type) {
		case *perffile.RecordSample:
			pidInfo := s.LookupPID(r.PID)
			mmap := pidInfo.LookupMmap(r.IP)
			if mmap == nil {
				break
			}

			samples++
			pr := prologueRanges(s, mmap)
			if _, _, _, ok := pr.Get(r.IP); ok {
				prologueSamples++
			}
		}
	}

	fmt.Printf("%d of %d samples (%.2f%%) in prologue\n", prologueSamples, samples, float64(prologueSamples)*100/float64(samples))
}

var prologueRangesKey = perfsession.NewExtraKey("prologuer.prologueRanges")

func prologueRanges(session *perfsession.Session, mmap *perfsession.Mmap) *perfsession.Ranges {
	fileTab, ok := session.Extra[prologueRangesKey].(map[string]*perfsession.Ranges)
	if !ok {
		fileTab = make(map[string]*perfsession.Ranges)
		session.Extra[prologueRangesKey] = fileTab
	}
	filename := mmap.Filename
	if r, ok := fileTab[filename]; ok {
		return r
	}

	if len(filename) > 0 && filename[0] == '[' {
		// Not a real file.
		return nil
	}

	r := new(perfsession.Ranges)
	fileTab[filename] = r

	elff, err := elf.Open(filename)
	if err != nil {
		log.Printf("error loading ELF file %s: %s", filename, err)
		return r
	}
	defer elff.Close()

	if elff.Section(".debug_info") == nil && elff.Section(".zdebug_info") == nil {
		// No DWARF info.
		return r
	}

	dwarff, err := elff.DWARF()
	if err != nil {
		log.Printf("error loading DWARF from %s: %s", filename, err)
		return r
	}

	pb, cb := fillRanges(r, dwarff)
	fmt.Printf("%s: %d of %d bytes (%.2f%%) in prologue\n", filename, pb, cb, float64(pb)*100/float64(cb))

	return r
}

func fillRanges(r *perfsession.Ranges, dwarff *dwarf.Data) (prologueBytes, codeBytes uint64) {
	dr := dwarff.Reader()
	for {
		ent, err := dr.Next()
		if ent == nil || err != nil {
			break
		}

		switch ent.Tag {
		default:
			dr.SkipChildren()

		case dwarf.TagModule, dwarf.TagNamespace:
			break

		case dwarf.TagCompileUnit:
			pend := prologueEndPCs(dwarff, ent)

			// Process functions in this CU.
			var funcRanges [][2]uint64
		cu:
			for {
				ent, err = dr.Next()
				if ent == nil || err != nil {
					break
				}

				switch ent.Tag {
				case 0:
					// End of children of the CU.
					break cu

				default:
					dr.SkipChildren()

				case dwarf.TagSubprogram:
					dr.SkipChildren()
					fr, err := dwarff.Ranges(ent)
					if err != nil {
						log.Fatal(err)
					}
					funcRanges = append(funcRanges, fr...)
				}
			}

			for _, fr := range funcRanges {
				codeBytes += fr[1] - fr[0]
			}

			// Match function ranges against prologue ends.
			sort.Slice(funcRanges, func(i, j int) bool {
				return funcRanges[i][0] < funcRanges[j][0]
			})
			for len(pend) > 0 && len(funcRanges) > 0 {
				if pend[0] < funcRanges[0][0] {
					pend = pend[1:]
				} else if pend[0] >= funcRanges[0][1] {
					funcRanges = funcRanges[1:]
				} else {
					r.Add(funcRanges[0][0], pend[0], nil)
					prologueBytes += pend[0] - funcRanges[0][0]
					pend = pend[1:]
					funcRanges = funcRanges[1:]
				}
			}
		}
	}
	return
}

func prologueEndPCs(dwarff *dwarf.Data, cu *dwarf.Entry) []uint64 {
	// Decode CU's line table to find prologue end
	// PCs
	var pend []uint64
	lr, err := dwarff.LineReader(cu)
	if err != nil {
		log.Fatal(err)
	} else if lr == nil {
		return nil
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
		if lent.PrologueEnd {
			pend = append(pend, lent.Address)
		}
	}

	return pend
}
