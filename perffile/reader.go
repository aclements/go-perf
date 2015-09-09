// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
)

// TODO: Type for file format errors.

// A File is a perf.data file. It consists of a sequence of records,
// which can be retrieved with the Records method, as well as several
// optional metadata fields.
type File struct {
	r      io.ReaderAt
	closer io.Closer
	hdr    fileHeader

	attrs    []fileAttr
	idToAttr map[attrID]*EventAttr

	featureSections map[feature]fileSection

	sampleIDOffset int // byte offset of AttrID in sample

	sampleIDAll    bool // non-samples have sample_id trailer
	recordIDOffset int  // byte offset of AttrID in non-sample, from end
}

// New reads a "perf.data" file from r.
//
// The caller must keep r open as long as it is using the returned
// *File.
func New(r io.ReaderAt) (*File, error) {
	file := &File{r: r}

	// See perf_session__read_header in tools/perf/util/header.c

	// Read header
	//
	// TODO: Support big endian
	sr := io.NewSectionReader(r, 0, 1024)
	if err := binary.Read(sr, binary.LittleEndian, &file.hdr); err != nil {
		return nil, err
	}
	if string(file.hdr.Magic[:]) != "PERFILE2" {
		return nil, fmt.Errorf("bad or unsupported file magic %q", string(file.hdr.Magic[:]))
	}
	if file.hdr.Size != uint64(binary.Size(&file.hdr)) {
		return nil, fmt.Errorf("bad header size %d", file.hdr.Size)
	}
	// TODO: perf supports attrSize 64, 72, 80, and 96 for compatibility
	if file.hdr.AttrSize != uint64(binary.Size(&fileAttr{})) {
		return nil, fmt.Errorf("bad attr size %d", file.hdr.AttrSize)
	}

	// hdr.Data.Size is the last thing written out by perf, so if
	// it's zero, we're working with a partial file.
	if file.hdr.Data.Size == 0 {
		return nil, fmt.Errorf("truncated data file")
	}

	// Read EventAttrs
	if err := readSlice(file.hdr.Attrs.sectionReader(r), &file.attrs); err != nil {
		return nil, err
	}
	// Check EventAttr sizes
	attrSize := uint32(binary.Size(&EventAttr{}))
	for _, attr := range file.attrs {
		if attr.Attr.Size != attrSize {
			return nil, fmt.Errorf("bad attr size %d", attr.Attr.Size)
		}
	}

	// Read EventAttr IDs and create ID -> EventAttr map
	file.idToAttr = make(map[attrID]*EventAttr)
	for _, attr := range file.attrs {
		var ids []attrID
		if err := readSlice(attr.IDs.sectionReader(r), &ids); err != nil {
			return nil, err
		}
		for _, id := range ids {
			file.idToAttr[id] = &attr.Attr
		}
	}

	// If there's just one event, samples may implicitly refer to
	// that event, in which case there may be no IDs.  Create a
	// synthetic ID of 0.
	if len(file.idToAttr) == 0 {
		if len(file.attrs) > 1 {
			return nil, fmt.Errorf("file has multiple EventAttrs, but no IDs")
		}
		if file.attrs[0].Attr.SampleFormat&(SampleFormatID|SampleFormatIdentifier) != 0 {
			return nil, fmt.Errorf("sample format has IDs, but events don't have IDs")
		}
		file.idToAttr[0] = &file.attrs[0].Attr
		file.sampleIDOffset = -1
		file.sampleIDAll = file.attrs[0].Attr.Flags&EventFlagSampleIDAll != 0
		file.recordIDOffset = -1
	} else {
		// Compute offset of AttrID fields
		file.sampleIDOffset = -1
		file.sampleIDAll = true
		file.recordIDOffset = -1
		for _, attr := range file.attrs {
			x := attr.Attr.SampleFormat.sampleIDOffset()
			if x == -1 {
				return nil, fmt.Errorf("events have no ID field")
			} else if file.sampleIDOffset == -1 {
				file.sampleIDOffset = x
			} else if file.sampleIDOffset != x {
				return nil, fmt.Errorf("events have incompatible ID offsets %d and %d", file.sampleIDOffset, x)
			}

			if attr.Attr.Flags&EventFlagSampleIDAll == 0 {
				file.sampleIDAll = false
				continue
			}
			x = attr.Attr.SampleFormat.recordIDOffset()
			if x == -1 {
				return nil, fmt.Errorf("records have no ID field")
			} else if file.recordIDOffset == -1 {
				file.recordIDOffset = x
			} else if file.recordIDOffset != x {
				return nil, fmt.Errorf("records have incompatible ID offsets %d and %d", file.recordIDOffset, x)
			}
		}
	}

	// Load feature section information
	sr = io.NewSectionReader(r, int64(file.hdr.Data.Offset+file.hdr.Data.Size), int64(numFeatureBits*binary.Size(fileSection{})))
	file.featureSections = make(map[feature]fileSection)
	for bit := feature(0); bit < feature(numFeatureBits); bit++ {
		if !file.hdr.hasFeature(bit) {
			continue
		}
		sec := fileSection{}
		if err := binary.Read(sr, binary.LittleEndian, &sec); err != nil {
			return nil, err
		}
		file.featureSections[bit] = sec
	}

	return file, nil
}

// Open opens the named "perf.data" file using os.Open.
//
// The caller must call f.Close() on the returned file when it is
// done.
func Open(name string) (*File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	ff, err := New(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	ff.closer = f
	return ff, nil
}

// Close closes the File.
//
// If the File was created using New directly instead of Open, Close
// has no effect.
func (f *File) Close() error {
	var err error
	if f.closer != nil {
		err = f.closer.Close()
		f.closer = nil
	}
	return err
}

// readSlice reads an entire section into a slice.  v must be a
// pointer to a slice; the slice itself may be nil.  The section size
// must be an exact multiple of the size of the element type of v.
func readSlice(sr *io.SectionReader, v interface{}) error {
	// Figure out slice value size
	vt := reflect.TypeOf(v)
	if vt.Kind() != reflect.Ptr || vt.Elem().Kind() != reflect.Slice {
		panic("v must be a pointer to a slice")
	}
	et := vt.Elem().Elem()
	esize := binary.Size(reflect.Zero(et).Interface())
	nelem := int(sr.Size() / int64(esize))
	if sr.Size()%int64(esize) != 0 {
		return fmt.Errorf("section size %d is not a multiple of element size %d", sr.Size(), esize)
	}

	// Create slice
	reflect.ValueOf(v).Elem().Set(reflect.MakeSlice(vt.Elem(), nelem, nelem))

	// Read in to slice
	return binary.Read(sr, binary.LittleEndian, v)
}

// TODO: Consider reading all of these features right when the file is
// loaded and just dumping them in a field of File for the user.

func (f *File) featureBuf(bit feature) (bufDecoder, bool, error) {
	sec, ok := f.featureSections[bit]
	if !ok {
		return bufDecoder{}, false, nil
	}
	data, err := sec.data(f.r)
	if err != nil {
		return bufDecoder{}, false, err
	}
	return bufDecoder{data, binary.LittleEndian}, true, nil
}

func (f *File) stringFeature(bit feature) (string, error) {
	bd, ok, err := f.featureBuf(bit)
	if !ok {
		return "", err
	}
	bd.u32() // Ignore length; string is \0-terminated
	return bd.cstring(), nil
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

// BuildIDs returns the list of build IDs in the profile's features
// header, or nil if unknown. Note that in "live mode" (e.g., a file
// written by perf inject), it's possible for build IDs to be
// introduced in the sample stream itself.
func (f *File) BuildIDs() ([]BuildIDInfo, error) {
	// See write_build_id in tools/perf/util/header.c.
	bd, ok, err := f.featureBuf(featureBuildID)
	if !ok {
		return nil, err
	}
	out := make([]BuildIDInfo, 0)
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
		out = append(out, bid)
		bd.buf = start[size:]
	}
	return out, nil
}

// Hostname returns the hostname of the machine that recorded this
// profile, or "" if unknown.
func (f *File) Hostname() (string, error) {
	return f.stringFeature(featureHostname)
}

// OSRelease returns the OS release of the machine that recorded this
// profile, or "" if unknown.
func (f *File) OSRelease() (string, error) {
	return f.stringFeature(featureOSRelease)
}

// Version returns the perf version that recorded this profile, or ""
// if unknown.
func (f *File) Version() (string, error) {
	return f.stringFeature(featureVersion)
}

// Arch returns the host architecture of the machine that recorded
// this profile, or "" if unknown.
func (f *File) Arch() (string, error) {
	return f.stringFeature(featureArch)
}

// NrCPUs returns the number of online and available CPUs, or 0, 0 if
// unknown.
func (f *File) NrCPUs() (online int, avail int, err error) {
	bd, ok, err := f.featureBuf(featureNrCpus)
	if !ok {
		return 0, 0, err
	}
	return int(bd.u32()), int(bd.u32()), nil
}

// CPUDesc returns a string describing the CPU of the machine that
// recorded this profile, or "" if unknown.
func (f *File) CPUDesc() (string, error) {
	return f.stringFeature(featureCPUDesc)
}

// CPUID returns the CPUID string of the machine that recorded this
// profile, or "" if unknown.
func (f *File) CPUID() (string, error) {
	return f.stringFeature(featureCPUID)
}

// TotalMem returns the total memory in bytes of the machine that
// recorded this profile, or 0 if unknown.
func (f *File) TotalMem() (int64, error) {
	bd, ok, err := f.featureBuf(featureTotalMem)
	if !ok {
		return 0, err
	}
	return int64(bd.u64()) * 1024, nil
}

// CmdLine returns the list of command line arguments perf was invoked
// with. If unknown, it returns nil, nil.
func (f *File) CmdLine() ([]string, error) {
	bd, ok, err := f.featureBuf(featureCmdline)
	if !ok {
		return nil, err
	}
	return bd.stringList(), nil
}

// TODO: Implement featureEventDesc. This isn't useful unless we also
// expose attribute IDs or something to make it possible to match up
// the event descriptions with the samples. Probably we should hide
// this as a feature section and just expose the set of events in the
// file, augmented with the string names from this section if
// available. As far as I can tell, the string name is the *only*
// thing this section adds over the EventAttrs in the file header.

// CPUTopology returns the CPU topology of the machine that recorded
// this profile, or nil, nil if unknown. Each CPUSet in coreGroups is
// a set of CPUs in the same package, and each CPUSet in threadGroups
// is a set of hardware threads in the same core.
func (f *File) CPUTopology() (coreGroups, threadGroups []CPUSet, err error) {
	bd, ok, err := f.featureBuf(featureCPUTopology)
	if !ok {
		return nil, nil, err
	}
	cores, threads := bd.stringList(), bd.stringList()
	coreGroups = make([]CPUSet, len(cores))
	for i, str := range cores {
		coreGroups[i], err = parseCPUSet(str)
		if err != nil {
			return nil, nil, err
		}
	}
	threadGroups = make([]CPUSet, len(threads))
	for i, str := range threads {
		threadGroups[i], err = parseCPUSet(str)
		if err != nil {
			return nil, nil, err
		}
	}
	return
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

// NUMATopology returns the NUMA topology of the machine that recorded
// this profile, or nil if unknown.
func (f *File) NUMATopology() ([]NUMANode, error) {
	bd, ok, err := f.featureBuf(featureNUMATopology)
	if !ok {
		return nil, err
	}
	count := bd.u32()
	out := []NUMANode{}
	for i := uint32(0); i < count; i++ {
		node := NUMANode{
			Node:     int(bd.u32()),
			MemTotal: int64(bd.u64()) * 1024,
			MemFree:  int64(bd.u64()) * 1024,
		}
		node.CPUs, err = parseCPUSet(bd.lenString())
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, nil
}

// PMUMappings returns a map from numerical EventClass to name for
// event classes supported by the machine that recorded this profile,
// or nil if unknown.
func (f *File) PMUMappings() (map[int]string, error) {
	bd, ok, err := f.featureBuf(featurePMUMappings)
	if !ok {
		return nil, err
	}
	count := bd.u32()
	out := map[int]string{}
	for i := uint32(0); i < count; i++ {
		out[int(bd.u32())] = bd.lenString()
	}
	return out, nil
}

// A GroupDesc describes a group of PMU events that are scheduled
// together.
type GroupDesc struct {
	Name       string
	Leader     int
	NumMembers int
}

// GroupDesc returns the descriptions of each perf event group, or nil
// if unknown.
func (f *File) GroupDesc() ([]GroupDesc, error) {
	bd, ok, err := f.featureBuf(featureGroupDesc)
	if !ok {
		return nil, err
	}
	count := bd.u32()
	out := []GroupDesc{}
	for i := uint32(0); i < count; i++ {
		out = append(out, GroupDesc{
			Name:       bd.lenString(),
			Leader:     int(bd.u32()),
			NumMembers: int(bd.u32()),
		})
	}
	return out, nil
}

//go:generate stringer -type=RecordsOrder

type RecordsOrder int

const (
	// RecordsFileOrder requests records in file order. This is
	// efficient because it allows streaming the records directly
	// from the file, but the records may not be in time-stamp or
	// even causal order.
	RecordsFileOrder RecordsOrder = iota

	// RecordsCausalOrder requests records in causal order. This
	// is weakly time-ordered: any two records will be in
	// time-stamp order *unless* those records are both
	// RecordSamples. This is potentially more efficient than
	// RecordsTimeOrder, though currently the implementation does
	// not distinguish.
	RecordsCausalOrder

	// RecordsTimeOrder requests records in time-stamp order. This
	// is the most expensive iteration order because it requires
	// buffering and/or re-reading potentially large sections of
	// the input file in order to sort the records.
	RecordsTimeOrder
)

// Records returns an iterator over the records in the profile. The
// order argument specifies the order for iterating through the
// records in this File. Callers should choose the least
// resource-intensive iteration order that satisfies their needs.
func (f *File) Records(order RecordsOrder) *Records {
	if order == RecordsCausalOrder || order == RecordsTimeOrder {
		// Sort the records by making two passes: first record
		// the offsets and time-stamps of all records, then
		// sort this by time-stamp and re-read in the new
		// offset order.
		//
		// See process_finished_round in session.c for how
		// perf does this. process_finished_round uses a
		// special flush event; however, I've never actually
		// observed in a perf.data file, so I think perf may
		// be reading and sorting the whole file looking for a
		// flush.

		// TODO: Optimize the first pass to decode only the
		// record length and time-stamp.

		// TODO: Optimize IO on the second pass by keeping
		// track of the non-monotonic boundaries and
		// performing separately buffered reads of each
		// sub-stream.

		rs := f.Records(RecordsFileOrder)
		pos, ts := make([]int64, 0), make([]uint64, 0)
		for rs.Next() {
			c := rs.Record.Common()
			pos = append(pos, c.Offset)
			ts = append(ts, c.Time)
		}
		if rs.Err() != nil {
			return &Records{err: rs.Err()}
		}
		sort.Stable(&timeSorter{pos, ts})
		return &Records{f: f, sr: newBufferedSectionReader(f.hdr.Data.sectionReader(f.r)), order: pos}
	}

	return &Records{f: f, sr: newBufferedSectionReader(f.hdr.Data.sectionReader(f.r))}
}

type timeSorter struct {
	pos []int64
	ts  []uint64
}

func (s *timeSorter) Len() int {
	return len(s.pos)
}

func (s *timeSorter) Less(i, j int) bool {
	return s.ts[i] < s.ts[j]
}

func (s *timeSorter) Swap(i, j int) {
	s.pos[i], s.pos[j] = s.pos[j], s.pos[i]
	s.ts[i], s.ts[j] = s.ts[j], s.ts[i]
}
