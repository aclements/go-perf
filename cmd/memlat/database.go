// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"

	"github.com/aclements/go-perf/perffile"
	"github.com/aclements/go-perf/perfsession"
)

type database struct {
	// procs maps from PID to information and records for a
	// process.
	procs map[int]*proc

	// dataSrcs maps dataSrcIDs to full DataSrc information.
	// There's a lot of information in a DataSrc, but in practice
	// a given architecture will generate a small subset of the
	// possibilities. Hence, rather than storing a whole DataSrc
	// in every record, we canonicalize it to a small identifier.
	dataSrcs []perffile.DataSrc

	// maxLatency is the maximum latency value across all records
	// in this database.
	maxLatency uint32

	// metadata records metadata fields from the profile.
	metadata Metadata
}

type proc struct {
	pid     int
	comm    string
	records []record
	ipInfo  map[uint64]ipInfo
}

type record struct {
	ip      uint64
	address uint64
	latency uint32
	dataSrc dataSrcID
}

type ipInfo struct {
	funcName string
	fileName string
	line     int
}

// dataSrcID is a small integer identifying a perffile.DataSrc.
type dataSrcID uint32

type Metadata struct {
	Hostname string
	Arch     string
	CPUDesc  string   `json:"CPU"`
	CmdLine  []string `json:"Command line"`
}

// parsePerf parses a perf.data profile into a database.
func parsePerf(fileName string) *database {
	f, err := perffile.Open(fileName)
	if os.IsNotExist(err) && fileName == "perf.data" {
		// Give a friendly error for first-time users.
		fmt.Fprintf(os.Stderr, "%s.\nTo record a profile, use\n  perf mem record <command>\nor specify an alternate profile path with -i.\n", err)
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "error loading profile: %s\n", err)
		os.Exit(1)
	}
	defer f.Close()

	db := &database{
		procs: make(map[int]*proc),
	}
	db.metadata.Hostname = f.Meta.Hostname
	db.metadata.Arch = f.Meta.Arch
	db.metadata.CPUDesc = f.Meta.CPUDesc
	db.metadata.CmdLine = f.Meta.CmdLine

	dataSrc2ID := make(map[perffile.DataSrc]dataSrcID)
	s := perfsession.New(f)

	numSamples := 0
	droppedMmaps := 0
	droppedSymbols := 0

	const requiredFormat = perffile.SampleFormatIP | perffile.SampleFormatAddr | perffile.SampleFormatDataSrc

	rs := f.Records(perffile.RecordsCausalOrder)
	for rs.Next() {
		r := rs.Record
		s.Update(r)

		switch r := r.(type) {
		case *perffile.RecordComm:
			// Comm events usually happen after the first
			// few samples from this PID.
			p := db.procs[r.PID]
			if p != nil {
				p.comm = r.Comm
			}

		case *perffile.RecordSample:
			if r.Format&requiredFormat != requiredFormat {
				break
			}
			// Either Weight or WeightStruct is required.
			if r.Format&(perffile.SampleFormatWeight|perffile.SampleFormatWeightStruct) == 0 {
				break
			}

			numSamples++

			pidInfo := s.LookupPID(r.PID)
			mmap := pidInfo.LookupMmap(r.IP)
			if mmap == nil {
				droppedMmaps++
				break
			}

			// Find proc for r.PID.
			p, ok := db.procs[r.PID]
			if !ok {
				p = &proc{
					pid:    r.PID,
					comm:   pidInfo.Comm,
					ipInfo: make(map[uint64]ipInfo),
				}
				db.procs[r.PID] = p
			}

			// Canonicalize data source.
			dsID, ok := dataSrc2ID[r.DataSrc]
			if !ok {
				dsID = dataSrcID(len(db.dataSrcs))
				dataSrc2ID[r.DataSrc] = dsID
				db.dataSrcs = append(db.dataSrcs, r.DataSrc)
			}

			// Create the record.
			p.records = append(p.records, record{
				ip:      r.IP,
				address: r.Addr,
				latency: uint32(r.Weight),
				dataSrc: dsID,
			})

			// Update database stats.
			if uint32(r.Weight) > db.maxLatency {
				db.maxLatency = uint32(r.Weight)
			}

			// Symbolize IP.
			if _, ok := p.ipInfo[r.IP]; !ok {
				// TODO: Intern strings
				var symb perfsession.Symbolic
				if !perfsession.Symbolize(s, mmap, r.IP, &symb) {
					droppedSymbols++
				}
				if symb.FuncName == "" {
					symb.FuncName = "[unknown]"
				}
				fileName := "[unknown]"
				if symb.Line.File != nil && symb.Line.File.Name != "" {
					fileName = symb.Line.File.Name
				}
				p.ipInfo[r.IP] = ipInfo{
					funcName: symb.FuncName,
					fileName: fileName,
					line:     symb.Line.Line,
				}
			}
		}
	}

	if numSamples == 0 {
		fmt.Printf("no memory latency samples in %s (did you use \"perf mem record\"?)\n", fileName)
		os.Exit(1)
	}
	if droppedMmaps > 0 {
		fmt.Printf("warning: %d sample IPs (%d%%) occurred in unmapped memory regions\n", droppedMmaps, droppedMmaps*100/numSamples)
	}
	if droppedSymbols > 0 {
		fmt.Printf("warning: failed to symbolize %d samples (%d%%)\n", droppedSymbols, droppedSymbols*100/numSamples)
	}

	return db
}

// filter specifies a set of field values to filter records on. The
// zero value of each field means not to filter on that field.
type filter struct {
	pid      int
	funcName string
	fileName string
	line     int // Requires fileName.
	address  uint64
	dataSrc  perffile.DataSrc
}

// filter invokes cb for every record matching f.
func (db *database) filter(f *filter, cb func(*proc, *record)) {
	dsFilter := f.dataSrc != perffile.DataSrc{}
	filterProc := func(proc *proc) {
		var ds perffile.DataSrc

		// TODO: Consider creating indexes for some or all of
		// these. Then just do a list merge of the record
		// indexes.
		for i := range proc.records {
			// Avoid heap-allocating for passing rec to cb.
			rec := &proc.records[i]
			if f.address != 0 && f.address != rec.address {
				continue
			}
			ipi := proc.ipInfo[rec.ip]
			if f.funcName != "" && f.funcName != ipi.funcName {
				continue
			}
			if f.fileName != "" && f.fileName != ipi.fileName {
				continue
			}
			if f.line != 0 && f.line != ipi.line {
				continue
			}
			if !dsFilter {
				// Short-circuit dataSrc checking.
				goto good
			}

			ds = db.dataSrcs[rec.dataSrc]
			if f.dataSrc.Op != 0 && f.dataSrc.Op != ds.Op {
				continue
			}
			if f.dataSrc.Level != 0 && (f.dataSrc.Level != ds.Level || f.dataSrc.Miss != ds.Miss) {
				continue
			}
			if f.dataSrc.Snoop != 0 && f.dataSrc.Snoop != ds.Snoop {
				continue
			}
			if f.dataSrc.Locked != 0 && f.dataSrc.Locked != ds.Locked {
				continue
			}
			if f.dataSrc.TLB != 0 && f.dataSrc.TLB != ds.TLB {
				continue
			}

		good:
			cb(proc, rec)
		}
	}

	if f.pid == 0 {
		for _, proc := range db.procs {
			filterProc(proc)
		}
	} else {
		proc := db.procs[f.pid]
		if proc != nil {
			filterProc(proc)
		}
	}
}
