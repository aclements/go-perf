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
	// Meta contains the metadata for this profile, such as
	// information about the hardware.
	Meta FileMeta

	// Events lists all events that may appear in this profile.
	Events []*EventAttr

	r      io.ReaderAt
	closer io.Closer
	hdr    fileHeader

	attrs    []fileAttr
	idToAttr map[attrID]*EventAttr

	sampleIDOffset int // byte offset of AttrID in sample

	sampleIDAll    bool // non-samples have sample_id trailer
	recordIDOffset int  // byte offset of AttrID in non-sample, from end
}

// New reads a "perf.data" file from r.
//
// The caller must keep r open as long as it is using the returned
// *File.
func New(r io.ReaderAt) (*File, error) {
	// See perf_session__open in tools/perf/util/session.c.
	file := &File{r: r, Events: make([]*EventAttr, 0)}

	// Read and process the file header.
	//
	// See perf_session__read_header in tools/perf/util/header.c

	sr := io.NewSectionReader(r, 0, 1024)
	if err := binary.Read(sr, binary.LittleEndian, &file.hdr); err != nil {
		return nil, err
	}
	switch string(file.hdr.Magic[:]) {
	case "PERFILE2":
		// Version 2, little endian.
		break
	case "2ELIFREP":
		// Version 2, big endian.
		//
		// TODO: Support big endian profiles.
		return nil, fmt.Errorf("big endian profiles not supported")
	case "PERFFILE":
		// Version 1 file.
		return nil, fmt.Errorf("version 1 profiles not supported")
	default:
		return nil, fmt.Errorf("bad or unsupported file magic %q", string(file.hdr.Magic[:]))
	}
	if file.hdr.Size != uint64(binary.Size(&file.hdr)) {
		return nil, fmt.Errorf("bad header size %d", file.hdr.Size)
	}

	// hdr.Data.Size is the last thing written out by perf, so if
	// it's zero, we're working with a partial file.
	if file.hdr.Data.Size == 0 {
		return nil, fmt.Errorf("truncated data file; was 'perf record' properly terminated?")
	}

	// Read EventAttrs. Note that the attr size is represented in
	// both the file header and in each individual attr, but perf
	// doesn't validate the file-level attr size.
	if file.hdr.AttrSize == 0 {
		return nil, fmt.Errorf("bad attr size 0")
	}
	nAttrs := int(file.hdr.Attrs.Size / file.hdr.AttrSize)
	if nAttrs == 0 {
		return nil, fmt.Errorf("no event types")
	} else if nAttrs > 64*1024 {
		return nil, fmt.Errorf("too many attrs or bad attr size")
	}
	file.attrs = make([]fileAttr, nAttrs)
	attrSR := file.hdr.Attrs.sectionReader(r)
	for i := 0; i < nAttrs; i++ {
		if err := readFileAttr(attrSR, &file.attrs[i]); err != nil {
			return nil, err
		}
		file.Events = append(file.Events, &file.attrs[i].Attr)
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

	// Check that sample formats are consistent across all event
	// types and record cross-event sample format information.
	firstEvent := &file.attrs[0].Attr
	file.sampleIDOffset = firstEvent.SampleFormat.sampleIDOffset()
	file.recordIDOffset = firstEvent.SampleFormat.recordIDOffset()
	file.sampleIDAll = firstEvent.Flags&EventFlagSampleIDAll != 0
	if len(file.attrs) > 1 {
		if len(file.idToAttr) == 0 {
			return nil, fmt.Errorf("file has multiple EventAttrs, but no IDs")
		}
		for _, attr := range file.attrs {
			// See perf_evlist__valid_sample_type.
			x := attr.Attr.SampleFormat.sampleIDOffset()
			if x == -1 {
				return nil, fmt.Errorf("multiple events, but samples have no event ID field")
			} else if file.sampleIDOffset != x {
				return nil, fmt.Errorf("events have incompatible ID offsets %d and %d", file.sampleIDOffset, x)
			}

			x = attr.Attr.SampleFormat.recordIDOffset()
			if x == -1 {
				return nil, fmt.Errorf("multiple events, but records have no event ID field")
			} else if file.recordIDOffset != x {
				return nil, fmt.Errorf("records have incompatible ID offsets %d and %d", file.recordIDOffset, x)
			}

			// See perf_evlist__valid_sample_id_all.
			idAll := attr.Attr.Flags&EventFlagSampleIDAll != 0
			if file.sampleIDAll != idAll {
				return nil, fmt.Errorf("events have incompatible SampleIDAll flags")
			}

			// See perf_evlist__valid_read_format.
			if firstEvent.ReadFormat != attr.Attr.ReadFormat {
				return nil, fmt.Errorf("events have incompatible read formats")
			}
		}
		if firstEvent.SampleFormat&SampleFormatRead != 0 &&
			firstEvent.ReadFormat&ReadFormatID == 0 {
			return nil, fmt.Errorf("bad event read format")
		}
	}

	// Load feature sections.
	sr = io.NewSectionReader(r, int64(file.hdr.Data.Offset+file.hdr.Data.Size), int64(numFeatureBits*binary.Size(fileSection{})))
	for bit := feature(0); bit < feature(numFeatureBits); bit++ {
		if !file.hdr.hasFeature(bit) {
			continue
		}
		sec := fileSection{}
		if err := binary.Read(sr, binary.LittleEndian, &sec); err != nil {
			return nil, err
		}
		file.Meta.parse(bit, sec, file.r)
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

func readFileAttr(sr *io.SectionReader, fa *fileAttr) error {
	// See read_attr in tools/perf/util/header.c.

	// Read the common prefix of all event attr versions.
	var attr eventAttrVN
	if err := binary.Read(sr, binary.LittleEndian, &attr.eventAttrV0); err != nil {
		return err
	}
	if attr.Size == 0 {
		// Assume ABI v0
		attr.Size = 64
	} else if attr.Size > uint32(binary.Size(&attr)) {
		return fmt.Errorf("event attr size %d too large; more recent and unsupported format", attr.Size)
	} else {
		// Read whatever's left. There are specific versions
		// of this structure, but perf doesn't try to
		// distinguish them, so neither do we.
		left := int(attr.Size) - binary.Size(&attr.eventAttrV0)
		rattr := reflect.ValueOf(&attr).Elem()
		for i := 1; i < rattr.NumField() && left > 0; i++ {
			field := rattr.Field(i).Addr().Interface()
			err := binary.Read(sr, binary.LittleEndian, field)
			if err != nil {
				return err
			}
			left -= binary.Size(field)
		}
	}

	// Convert on-disk perf_event_attr in to EventAttr.
	fa.Attr.Type = attr.Type
	fa.Attr.Config[0] = attr.Config
	if attr.Flags&EventFlagFreq == 0 {
		fa.Attr.SamplePeriod = attr.SamplePeriodOrFreq
	} else {
		fa.Attr.SampleFreq = attr.SamplePeriodOrFreq
	}
	fa.Attr.SampleFormat = attr.SampleFormat
	fa.Attr.ReadFormat = attr.ReadFormat
	fa.Attr.Flags = attr.Flags &^ eventFlagPreciseMask
	fa.Attr.Precise = EventPrecision((attr.Flags & eventFlagPreciseMask) >> eventFlagPreciseShift)
	if attr.Flags&EventFlagWakeupWatermark == 0 {
		fa.Attr.WakeupEvents = attr.WakeupEventsOrWatermark
	} else {
		fa.Attr.WakeupWatermark = attr.WakeupEventsOrWatermark
	}
	fa.Attr.BPType = attr.BPType
	if attr.Type == EventTypeBreakpoint {
		fa.Attr.BPAddr = attr.BPAddrOrConfig1
		fa.Attr.BPLen = attr.BPLenOrConfig2
	} else {
		fa.Attr.Config[1] = attr.BPAddrOrConfig1
		fa.Attr.Config[2] = attr.BPLenOrConfig2
	}
	fa.Attr.SampleRegsUser = attr.SampleRegsUser
	fa.Attr.SampleStackUser = attr.SampleStackUser
	fa.Attr.AuxWatermark = attr.AuxWatermark

	// Finally, read IDs fileSection, which follows the eventAttr.
	return binary.Read(sr, binary.LittleEndian, &fa.IDs)
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
