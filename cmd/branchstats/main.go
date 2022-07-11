// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command branchstats analyzes branch profiles for branch mispredict
// rates.
//
// branchstats expects a perf.data collected with
//
//     perf record -e branches -j any -c 400009
//
// To collect only branches in user-space code, use
//
//     perf record -e branches:u -j any,u -c 400009
//
// The output is a table like
//
//     comm     PC                               branches mispredicts
//     bench    scanner.go:258                  419609441 309206957 (73.7%)
//         257 func isLetter(ch rune) bool {
//         258         return 'a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z' || ch == '_' || ch >= 0x80 && unicode.IsLetter(ch)
//         259 }
//
//     bench    mgcmark.go:1000                1967244262 236405319 (12.0%)
//         999 }
//        1000 if bits&bitPointer == 0 {
//        1001         continue // not a pointer
//
// Each row shows a branch at a particular location and gives the
// estimated number of times that branch executed, the estimated
// number of mispredicts, and the mispredict rate. The table is sorted
// by the number of mispredicts.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/aclements/go-perf/perffile"
	"github.com/aclements/go-perf/perfsession"
)

type PC struct {
	PC   uint64
	Comm string
}

type Agg struct {
	Mmap         *perfsession.Mmap
	Events       uint64
	Predicted    int64
	Mispredicted int64
}

type pair struct {
	PC
	Agg
	rate float64
}

func main() {
	var (
		flagInput = flag.String("i", "perf.data", "input perf.data `file`")
	)
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

	agg := make(map[PC]Agg)

	const requiredFormat = perffile.SampleFormatTID | perffile.SampleFormatBranchStack
	rs := f.Records(perffile.RecordsCausalOrder)
	for rs.Next() {
		r := rs.Record
		s.Update(r)

		switch r := r.(type) {
		case *perffile.RecordSample:
			if r.Format&requiredFormat != requiredFormat {
				break
			}

			pidinfo := s.LookupPID(r.PID)
			comm := "<unknown>"
			var mmap *perfsession.Mmap
			if pidinfo != nil {
				comm = pidinfo.Comm
				mmap = pidinfo.LookupMmap(r.BranchStack[0].From)
			}

			// We ignore the location of the sample
			// because it's often not a branch (even in
			// precise mode). Instead, we take the most
			// recent branch record as an unbiased,
			// precise sampling of branches. Similarly, we
			// only take the prediction information from
			// the most recent branch. (Too bad there's no
			// way to tell perf we only want one branch
			// record.)
			br := r.BranchStack[0]
			pc := PC{br.From, comm}

			var events uint64
			if r.Format&perffile.SampleFormatPeriod != 0 {
				events = r.Period
			} else if r.EventAttr.Flags&perffile.EventFlagFreq == 0 {
				events = r.EventAttr.SamplePeriod
			} else {
				log.Fatalf("sample %+v has no period", r)
			}

			a := agg[pc]
			a.Events += events
			a.Mmap = mmap
			if br.Flags&perffile.BranchFlagMispredicted != 0 {
				a.Mispredicted++
			}
			if br.Flags&perffile.BranchFlagPredicted != 0 {
				a.Predicted++
			}
			agg[pc] = a
		}
	}
	if err := rs.Err(); err != nil {
		log.Fatal(err)
	}

	// Rescale and sort records.
	pairs := make([]pair, 0)
	for pc, a := range agg {
		if a.Events == 0 {
			continue
		}
		rate := float64(a.Mispredicted) / float64(a.Predicted+a.Mispredicted)
		a.Mispredicted = int64(rate * float64(a.Events))
		a.Predicted = int64(a.Events) - a.Mispredicted

		pairs = append(pairs, pair{pc, a, rate})
	}
	sort.Sort(sort.Reverse(pairSorter(pairs)))

	// Print summary information.
	var total Agg
	for _, a := range pairs {
		total.Events += a.Events
		total.Mispredicted += a.Mispredicted
		total.Predicted += a.Predicted
	}
	fmt.Printf("# Total branches: %d\n", total.Events)
	fmt.Printf("# Total mispredicts: %d (%2.1f%% of all branches)\n", total.Mispredicted, 100*float64(total.Mispredicted)/float64(total.Events))
	fmt.Printf("\n")

	// Print branch details.
	var sym perfsession.Symbolic
	fmt.Printf("%-8s %-24s %16s %s\n", "comm", "PC", "branches", "mispredicts")
	for _, pair := range pairs {
		var pos string
		var lines []string
		if pair.Mmap != nil && perfsession.Symbolize(s, pair.Mmap, pair.PC.PC, &sym) && sym.Line.File != nil {
			pos = fmt.Sprintf("%s:%d", filepath.Base(sym.Line.File.Name), sym.Line.Line)
			lines, _ = getLines(sym.Line.File.Name, sym.Line.Line-1, sym.Line.Line+1)
		} else {
			pos = fmt.Sprintf("%#-24x", pair.PC.PC)
			lines = nil
		}

		fmt.Printf("%-8.8s %-24s %16d %d (%2.1f%%)\n", pair.Comm, pos, pair.Events, pair.Mispredicted, 100*pair.rate)
		trim := stringCommon(lines)
		for i, line := range lines {
			fmt.Printf("%7d %s\n", i+sym.Line.Line-1, line[trim:])
		}
		fmt.Printf("\n")
	}
}

type pairSorter []pair

func (p pairSorter) Len() int {
	return len(p)
}

func (p pairSorter) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func (p pairSorter) Less(i, j int) bool {
	if p[i].Mispredicted != p[j].Mispredicted {
		return p[i].Mispredicted < p[j].Mispredicted
	}
	if p[i].Events != p[j].Events {
		return p[i].Events < p[j].Events
	}
	if p[i].Comm != p[j].Comm {
		return p[i].Comm < p[j].Comm
	}
	return p[i].PC.PC < p[j].PC.PC
}

func getLines(path string, minLine, maxLine int) ([]string, error) {
	// TODO: Make a nice line cache API. This isn't the only place
	// I've needed this.

	lines := make([]string, maxLine-minLine+1)

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Skip to minLine.
	scanner := bufio.NewScanner(file)
	for i := 0; i < minLine && scanner.Scan(); i++ {
		// Do nothing
	}

	for line := minLine; line <= maxLine && scanner.Err() == nil; line++ {
		lines[line-minLine] = scanner.Text()
		scanner.Scan()
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil
}

func stringCommon(strs []string) int {
	if len(strs) == 0 {
		return 0
	}

	for i := 0; i < len(strs[0]); i++ {
		c := strs[0][i]
		for _, s := range strs {
			if i == len(s) || s[i] != c {
				return i
			}
		}
	}
	return len(strs[0])
}
