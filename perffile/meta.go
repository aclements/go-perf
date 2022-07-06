// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
)

type FileMeta struct {
	// BuildIDs is the list of build IDs for processes and
	// libraries in this profile, or nil if unknown. Note that in
	// "live mode" (e.g., a file written by perf inject), it's
	// possible for build IDs to be introduced in the sample
	// stream itself.
	BuildIDs []BuildIDInfo

	// Hostname is the hostname of the machine that recorded this
	// profile, or "" if unknown.
	Hostname string

	// OSRelease is the OS release of the machine that recorded
	// this profile such as "3.13.0-62", or "" if unknown.
	OSRelease string

	// Version is the perf version that recorded this profile such
	// as "3.13.11", or "" if unknown.
	Version string

	// Arch is the host architecture of the machine that recorded
	// this profile such as "x86_64", or "" if unknown.
	Arch string

	// CPUsOnline and CPUsAvail are the number of online and
	// available CPUs of the machine that recorded this profile,
	// or 0, 0 if unknown.
	CPUsOnline, CPUsAvail int

	// CPUDesc describes the CPU of the machine that recorded this
	// profile such as "Intel(R) Core(TM) i7-4600U CPU @ 2.10GHz",
	// or "" if unknown.
	CPUDesc string

	// CPUID describes the CPU type of the machine that recorded
	// this profile, or "" if unknown. The exact format of this
	// varies between architectures. On x86 architectures, it is a
	// comma-separated list of vendor, family, model, and
	// stepping, such as "GenuineIntel,6,69,1".
	CPUID string

	// TotalMem is the total memory in bytes of the machine that
	// recorded this profile, or 0 if unknown.
	TotalMem int64

	// CmdLine is the list of command line arguments perf was
	// invoked with, or nil if unknown.
	CmdLine []string

	// CoreGroups and ThreadGroups describe the CPU topology of
	// the machine that recorded this profile. Each CPUSet in
	// CoreGroups is a set of CPUs in the same package, and each
	// CPUSet in ThreadGroups is a set of hardware threads in the
	// same core. These will be nil if unkneon.
	CoreGroups, ThreadGroups []CPUSet

	// NUMANodes is the set of NUMA nodes in the NUMA topology of
	// the machine that recorded this profile, or nil if unknown.
	NUMANodes []NUMANode

	// PMUMappings is a map from numerical PMUTypeID to name for
	// event classes supported by the machine that recorded this
	// profile, or nil if unknown.
	PMUMappings map[PMUTypeID]string

	// Groups is the descriptions of each perf event group in this
	// profile, or nil if unknown.
	Groups []GroupDesc
}

// A BuildIDInfo records the mapping between a single build ID and the
// path of an executable with that build ID.
type BuildIDInfo struct {
	CPUMode  CPUMode
	PID      int // Usually -1; for VM kernels
	BuildID  BuildID
	Filename string
}

type BuildID []byte

func (b BuildID) String() string {
	return fmt.Sprintf("%x", []byte(b))
}

// A NUMANode represents a single hardware NUMA node.
type NUMANode struct {
	// Node is the system identifier of this NUMA node.
	Node int

	// MemTotal and MemFree are the total and free number of bytes
	// of memory in this NUMA node.
	MemTotal, MemFree int64

	// CPUs is the set of CPUs in this NUMA node.
	CPUs CPUSet
}

// A GroupDesc describes a group of PMU events that are scheduled
// together.
//
// TODO: Are Leader and NumMembers attribute IDs? If so, we should
// probably map them to *EventAttrs to make this useful.
type GroupDesc struct {
	Name       string
	Leader     int
	NumMembers int
}

var featureParsers = map[feature]func(*FileMeta, bufDecoder) error{
	featureBuildID:      (*FileMeta).parseBuildID,
	featureHostname:     stringFeature("Hostname"),
	featureOSRelease:    stringFeature("OSRelease"),
	featureVersion:      stringFeature("Version"),
	featureArch:         stringFeature("Arch"),
	featureNrCpus:       (*FileMeta).parseNrCPUs,
	featureCPUDesc:      stringFeature("CPUDesc"),
	featureCPUID:        stringFeature("CPUID"),
	featureTotalMem:     (*FileMeta).parseTotalMem,
	featureCmdline:      (*FileMeta).parseCmdLine,
	featureCPUTopology:  (*FileMeta).parseCPUTopology,
	featureNUMATopology: (*FileMeta).parseNUMATopology,
	featurePMUMappings:  (*FileMeta).parsePMUMappings,
	featureGroupDesc:    (*FileMeta).parseGroupDesc,
}

func (m *FileMeta) parse(f feature, sec fileSection, r io.ReaderAt) error {
	parser := featureParsers[f]
	if parser == nil {
		return nil
	}

	// Load the section.
	data, err := sec.data(r)
	if err != nil {
		return err
	}
	bd := bufDecoder{data, binary.LittleEndian}

	// Parse the section.
	return parser(m, bd)
}

func stringFeature(name string) func(*FileMeta, bufDecoder) error {
	return func(m *FileMeta, bd bufDecoder) error {
		bd.u32() // Ignore length; string is \0-terminated
		str := bd.cstring()
		reflect.ValueOf(m).Elem().FieldByName(name).SetString(str)
		return nil
	}
}

func (m *FileMeta) parseBuildID(bd bufDecoder) error {
	m.BuildIDs = make([]BuildIDInfo, 0)
	for len(bd.buf) > 0 {
		var bid BuildIDInfo
		start := bd.buf
		// This starts with a recordHeader.
		_ = bd.u32() // type, unused
		bid.CPUMode = CPUMode(bd.u16() & uint16(recordMiscCPUModeMask))
		size := bd.u16()
		bid.PID = int(bd.i32())
		// The build ID is 20 bytes, but padded to 8 bytes.
		buildID := make([]byte, 24)
		bd.bytes(buildID)
		bid.BuildID = BuildID(buildID[:20])
		bid.Filename = bd.cstring()
		m.BuildIDs = append(m.BuildIDs, bid)
		bd.buf = start[size:]
	}
	return nil
}

func (m *FileMeta) parseNrCPUs(bd bufDecoder) error {
	m.CPUsOnline, m.CPUsAvail = int(bd.u32()), int(bd.u32())
	return nil
}

func (m *FileMeta) parseTotalMem(bd bufDecoder) error {
	m.TotalMem = int64(bd.u64()) * 1024
	return nil
}

func (m *FileMeta) parseCmdLine(bd bufDecoder) error {
	m.CmdLine = bd.stringList()
	return nil
}

// TODO: Implement featureEventDesc. This isn't useful unless we also
// expose attribute IDs or something to make it possible to match up
// the event descriptions with the samples. Probably we should hide
// this as a feature section and just expose the set of events in the
// file, augmented with the string names from this section if
// available. As far as I can tell, the string name is the *only*
// thing this section adds over the EventAttrs in the file header.

func (m *FileMeta) parseCPUTopology(bd bufDecoder) error {
	var err error
	cores, threads := bd.stringList(), bd.stringList()
	m.CoreGroups = make([]CPUSet, len(cores))
	for i, str := range cores {
		m.CoreGroups[i], err = parseCPUSet(str)
		if err != nil {
			return err
		}
	}
	m.ThreadGroups = make([]CPUSet, len(threads))
	for i, str := range threads {
		m.ThreadGroups[i], err = parseCPUSet(str)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *FileMeta) parseNUMATopology(bd bufDecoder) error {
	var err error
	count := bd.u32()
	m.NUMANodes = []NUMANode{}
	for i := uint32(0); i < count; i++ {
		node := NUMANode{
			Node:     int(bd.u32()),
			MemTotal: int64(bd.u64()) * 1024,
			MemFree:  int64(bd.u64()) * 1024,
		}
		node.CPUs, err = parseCPUSet(bd.lenString())
		if err != nil {
			return err
		}
		m.NUMANodes = append(m.NUMANodes, node)
	}
	return nil
}

func (m *FileMeta) parsePMUMappings(bd bufDecoder) error {
	count := bd.u32()
	m.PMUMappings = map[PMUTypeID]string{}
	for i := uint32(0); i < count; i++ {
		m.PMUMappings[PMUTypeID(bd.u32())] = bd.lenString()
	}
	return nil
}

func (m *FileMeta) parseGroupDesc(bd bufDecoder) error {
	count := bd.u32()
	m.Groups = []GroupDesc{}
	for i := uint32(0); i < count; i++ {
		m.Groups = append(m.Groups, GroupDesc{
			Name:       bd.lenString(),
			Leader:     int(bd.u32()),
			NumMembers: int(bd.u32()),
		})
	}
	return nil
}
