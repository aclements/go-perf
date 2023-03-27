// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"encoding/binary"
	"fmt"
	"io"
)

// A Records is an iterator over the records in a "perf.data" file.
// Each record will be one of the Record* types.
//
// Typical usage is
//
//	rs := file.Records()
//	for rs.Next() {
//	  switch r := rs.Record.(type) {
//	  case *perffile.RecordSample:
//	    ...
//	  }
//	}
//	if rs.Err() { ... }
type Records struct {
	// The current record. The concrete type of this will be one
	// of the Record* types. Determine which type of record this
	// is using a type switch.
	Record Record

	f   *File
	sr  *bufferedSectionReader // or *io.SectionReader
	err error

	// order specifies the seek order to read records in. If nil,
	// records are read in file order until EOF. If non-nil,
	// records are read in this order.
	order []int64

	// Read buffer.  Reused (and resized) by Next.
	buf []byte

	// Cache for common record types
	recordMmap          RecordMmap
	recordComm          RecordComm
	recordExit          RecordExit
	recordFork          RecordFork
	recordSample        RecordSample
	recordAux           RecordAux
	recordSwitch        RecordSwitch
	recordSwitchCPUWide RecordSwitchCPUWide
}

// Err returns the first error encountered by Records.
func (r *Records) Err() error {
	return r.err
}

// Next fetches the next record into r.Record.  It returns true if
// successful, and false if it reaches the end of the record stream or
// encounters an error.
//
// The record stored in r.Record may be reused by later invocations of
// Next, so if the caller may need the record after another call to
// Next, it must make its own copy.
func (r *Records) Next() bool {
	// See perf_evsel__parse_sample
	if r.err != nil {
		return false
	}

	if r.order != nil {
		if len(r.order) == 0 {
			return false
		}
		pos := r.order[0]
		r.order = r.order[1:]
		_, r.err = r.sr.Seek(pos-int64(r.f.hdr.Data.Offset), 0)
		if r.err != nil {
			return false
		}
	}

	var common RecordCommon
	offset, _ := r.sr.Seek(0, 1)
	common.Offset = offset + int64(r.f.hdr.Data.Offset)

	// Read record header
	var hdr recordHeader
	if err := binary.Read(r.sr, binary.LittleEndian, &hdr); err != nil {
		if err != io.EOF {
			r.err = err
		}
		return false
	}

	// Read record data
	rlen := int(hdr.Size - 8)
	if rlen > len(r.buf) {
		r.buf = make([]byte, rlen)
	}
	var bd = &bufDecoder{r.buf[:rlen], binary.LittleEndian}
	if _, err := io.ReadFull(r.sr, bd.buf); err != nil {
		r.err = err
		return false
	}

	// Parse common sample_id fields
	if r.f.sampleIDAll && hdr.Type != RecordTypeSample && hdr.Type < recordTypeUserStart {
		// mmap records in the prologue don't have eventAttrs
		// in recent perf versions, but that's okay.
		//
		// TODO: When is perf okay with missing eventAttrs?
		r.parseCommon(bd, &common, hdr.Type == RecordTypeMmap)
	}

	// Parse record
	// TODO: Don't array out-of-bounds on short records
	switch hdr.Type {
	default:
		// As far as I can tell, RecordTypeRead can never
		// appear in a perf.data file.
		r.Record = &RecordUnknown{hdr, common, bd.buf}

	case RecordTypeMmap:
		r.Record = r.parseMmap(bd, &hdr, &common, false)

	case RecordTypeLost:
		r.Record = r.parseLost(bd, &hdr, &common)

	case RecordTypeComm:
		r.Record = r.parseComm(bd, &hdr, &common)

	case RecordTypeExit:
		r.Record = r.parseExit(bd, &hdr, &common)

	case RecordTypeThrottle:
		r.Record = r.parseThrottle(bd, &hdr, &common, true)

	case RecordTypeUnthrottle:
		r.Record = r.parseThrottle(bd, &hdr, &common, false)

	case RecordTypeFork:
		r.Record = r.parseFork(bd, &hdr, &common)

	case RecordTypeSample:
		r.Record = r.parseSample(bd, &hdr, &common)

	case recordTypeMmap2:
		r.Record = r.parseMmap(bd, &hdr, &common, true)

	case RecordTypeAux:
		r.Record = r.parseAux(bd, &hdr, &common)

	case RecordTypeItraceStart:
		r.Record = r.parseItraceStart(bd, &hdr, &common)

	case RecordTypeLostSamples:
		r.Record = r.parseLostSamples(bd, &hdr, &common)

	case RecordTypeSwitch:
		r.Record = r.parseSwitch(bd, &hdr, &common)

	case RecordTypeSwitchCPUWide:
		r.Record = r.parseSwitchCPUWide(bd, &hdr, &common)

	case RecordTypeNamespaces:
		r.Record = r.parseNamespaces(bd, &hdr, &common)

	case RecordTypeKsymbol:
		r.Record = r.parseKsymbol(bd, &hdr, &common)

	case RecordTypeBPFEvent:
		r.Record = r.parseBPFEvent(bd, &hdr, &common)

	case RecordTypeCGroup:
		r.Record = r.parseCGroup(bd, &hdr, &common)

	case RecordTypeTextPoke:
		r.Record = r.parseTextPoke(bd, &hdr, &common)

	case RecordTypeAuxOutputHardwareID:
		r.Record = r.parseAuxOutputHardwareID(bd, &hdr, &common)

	case RecordTypeAuxtraceInfo:
		r.Record = r.parseAuxtraceInfo(bd, &hdr, &common)

	case RecordTypeAuxtrace:
		// Note: This appears to be the only record type that
		// has additional payload data following it that isn't
		// included in the header size.
		r.Record = r.parseAuxtrace(bd, &hdr, &common)
	}
	if r.err != nil {
		return false
	}
	return true
}

func (r *Records) getAttr(id attrID, nilOk bool) *EventAttr {
	// See perf_evlist__id2evsel in tools/perf/util/evlist.c.

	// If there's only one event, all records implicitly use it.
	if len(r.f.attrs) == 1 || id == 0 {
		return &r.f.attrs[0].Attr
	}
	// Otherwise, look up the event by ID.
	if attr, ok := r.f.idToAttr[id]; ok {
		return attr
	}
	if !nilOk {
		r.err = fmt.Errorf("event has unknown eventAttr ID %d", id)
	}
	return nil
}

// parseCommon parses the common sample_id structure in the trailer of
// non-sample records.
func (r *Records) parseCommon(bd *bufDecoder, o *RecordCommon, missingOk bool) bool {
	// Get EventAttr ID
	if r.f.recordIDOffset == -1 {
		o.ID = 0
	} else {
		o.ID = attrID(bd.order.Uint64(bd.buf[len(bd.buf)+r.f.recordIDOffset:]))
	}
	o.EventAttr = r.getAttr(o.ID, missingOk && o.ID == 0)
	if o.EventAttr == nil {
		return false
	}

	// Narrow decoder to the trailer
	commonLen := o.EventAttr.SampleFormat.trailerBytes()
	bd = &bufDecoder{bd.buf[len(bd.buf)-commonLen:], bd.order}

	// Decode trailer
	t := o.EventAttr.SampleFormat
	o.Format = t
	o.PID = int(bd.i32If(t&SampleFormatTID != 0))
	o.TID = int(bd.i32If(t&SampleFormatTID != 0))
	o.Time = bd.u64If(t&SampleFormatTime != 0)
	bd.u64If(t&SampleFormatID != 0)
	o.StreamID = bd.u64If(t&SampleFormatStreamID != 0)
	o.CPU = bd.u32If(t&SampleFormatCPU != 0)
	o.Res = bd.u32If(t&SampleFormatCPU != 0)
	return true
}

func (r *Records) parseMmap(bd *bufDecoder, hdr *recordHeader, common *RecordCommon, v2 bool) Record {
	o := &r.recordMmap
	o.RecordCommon = *common
	o.Format |= SampleFormatTID

	// Decode hdr.Misc
	o.Data = (hdr.Misc&recordMiscMmapData != 0)

	// Decode fields. Note that perf calls the file offset
	// "pgoff", but it's actually a byte offset.
	o.PID, o.TID = int(bd.i32()), int(bd.i32())
	o.Addr, o.Len, o.FileOffset = bd.u64(), bd.u64(), bd.u64()
	if v2 {
		buildID := (hdr.Misc&recordMiscMmapBuildID != 0)
		if buildID {
			buildIDLen := int(bd.u8())
			if o.BuildID == nil || cap(o.BuildID) < buildIDLen {
				o.BuildID = make([]byte, buildIDLen)
			} else {
				o.BuildID = o.BuildID[:buildIDLen]
			}
			bd.skip(3)
			bd.bytes(o.BuildID)

			o.Major, o.Minor = 0, 0
			o.Ino, o.InoGeneration = 0, 0
		} else {
			o.Major, o.Minor = bd.u32(), bd.u32()
			o.Ino, o.InoGeneration = bd.u64(), bd.u64()

			o.BuildID = nil
		}

		o.Prot, o.Flags = bd.u32(), bd.u32()
	}
	o.Filename = bd.cstring()

	return o
}

func (r *Records) parseLost(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordLost{RecordCommon: *common}
	o.Format |= SampleFormatID

	o.ID = attrID(bd.u64())
	o.EventAttr = r.getAttr(o.ID, false)
	o.NumLost = bd.u64()

	return o
}

func (r *Records) parseComm(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordComm
	o.RecordCommon = *common
	o.Format |= SampleFormatTID

	// Decode hdr.Misc
	o.Exec = (hdr.Misc&recordMiscCommExec != 0)

	// Decode fields
	o.PID, o.TID = int(bd.i32()), int(bd.i32())
	o.Comm = bd.cstring()

	return o
}

func (r *Records) parseExit(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordExit
	o.RecordCommon = *common
	o.Format |= SampleFormatTID | SampleFormatTime

	o.PID, o.PPID = int(bd.i32()), int(bd.i32())
	o.TID, o.PTID = int(bd.i32()), int(bd.i32())
	o.Time = bd.u64()

	return o
}

func (r *Records) parseThrottle(bd *bufDecoder, hdr *recordHeader, common *RecordCommon, enable bool) Record {
	o := &RecordThrottle{RecordCommon: *common, Enable: enable}
	o.Format |= SampleFormatTime | SampleFormatID | SampleFormatStreamID

	o.Time = bd.u64()
	// Throttle events always have an event attr ID, even if the
	// IDs aren't recorded.  So if we see an unknown attr ID, just
	// assume it's the default event.
	id := attrID(bd.u64())
	if r.f.idToAttr[id] == nil && r.f.idToAttr[0] != nil {
		o.EventAttr = r.f.idToAttr[0]
	} else {
		o.EventAttr = r.getAttr(id, false)
	}
	o.StreamID = bd.u64()

	return o
}

func (r *Records) parseFork(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordFork
	o.RecordCommon = *common
	o.Format |= SampleFormatTID | SampleFormatTime

	o.PID, o.PPID = int(bd.i32()), int(bd.i32())
	o.TID, o.PTID = int(bd.i32()), int(bd.i32())
	o.Time = bd.u64()

	return o
}

func (r *Records) parseAux(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordAux
	o.RecordCommon = *common

	o.Offset, o.Size = bd.u64(), bd.u64()

	flags := bd.u64()
	format := (flags & 0xff00) >> 8
	flags &^= 0xff00

	o.Flags = AuxFlags(flags)
	o.PMUFormat = AuxPMUFormat(format)

	return o
}

func (r *Records) parseItraceStart(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordItraceStart{RecordCommon: *common}
	o.Format |= SampleFormatTID
	o.PID, o.TID = int(bd.i32()), int(bd.i32())
	return o
}

func (r *Records) parseLostSamples(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordLostSamples{RecordCommon: *common}
	o.Lost = bd.u64()
	return o
}

func (r *Records) parseSwitch(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordSwitch
	o.RecordCommon = *common
	o.Out = hdr.Misc&recordMiscSwitchOut != 0
	return o
}

func (r *Records) parseSwitchCPUWide(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordSwitchCPUWide
	o.RecordCommon = *common
	o.SwitchPID, o.SwitchTID = int(bd.i32()), int(bd.i32())
	o.Out = hdr.Misc&recordMiscSwitchOut != 0
	o.Preempt = hdr.Misc&recordMiscSwitchOutPreempt != 0
	return o
}

func (r *Records) parseNamespaces(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordNamespaces{RecordCommon: *common}
	o.Format |= SampleFormatTID
	o.PID, o.TID = int(bd.i32()), int(bd.i32())
	n := bd.u64()
	o.Namespaces = make([]Namespace, n)
	for i := range o.Namespaces {
		o.Namespaces[i] = Namespace{bd.u64(), bd.u64()}
	}
	return o
}

func (r *Records) parseKsymbol(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordKsymbol{RecordCommon: *common}
	o.Addr, o.Len = bd.u64(), bd.u32()
	o.KsymType = KsymbolType(bd.u16())
	o.Flags = KsymbolFlags(bd.u64())
	o.Name = bd.cstring()

	return o
}

func (r *Records) parseBPFEvent(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordBPFEvent{RecordCommon: *common}
	o.EventType = BPFEventType(bd.u16())
	o.Flags = BPFEventFlags(bd.u16())
	o.ID = bd.u32()
	o.Tag = bd.u64()

	return o
}

func (r *Records) parseCGroup(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordCGroup{RecordCommon: *common}
	o.ID = bd.u32()
	o.Path = bd.cstring()

	return o
}

func (r *Records) parseTextPoke(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordTextPoke{RecordCommon: *common}
	o.Addr = bd.u64()

	oldLen, newLen := bd.u16(), bd.u16()

	o.Old = make([]byte, oldLen)
	bd.bytes(o.Old)

	o.New = make([]byte, newLen)
	bd.bytes(o.New)

	return o
}

func (r *Records) parseAuxOutputHardwareID(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordAuxOutputHardwareID{RecordCommon: *common}
	o.ID = bd.u64()
	return o
}

func (r *Records) parseAuxtraceInfo(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordAuxtraceInfo{RecordCommon: *common}
	o.Kind = bd.u32()
	bd.u32() // Alignment
	// TODO: Decode remainder according to Kind
	o.Priv = make([]uint64, len(bd.buf)/8)
	bd.u64s(o.Priv)
	return o
}

func (r *Records) parseAuxtrace(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordAuxtrace{RecordCommon: *common}
	size := bd.u64()
	o.Offset, o.Ref = bd.u64(), bd.u64()
	o.Idx, o.TID, o.CPU = bd.u32(), int(bd.u32()), bd.u32()
	o.Data = make([]byte, size)
	if _, err := io.ReadFull(r.sr, o.Data); err != nil {
		r.err = err
		return nil
	}
	return o
}

func (r *Records) parseSample(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &r.recordSample
	o.RecordCommon = *common

	// Get sample EventAttr ID
	if r.f.sampleIDOffset == -1 {
		o.ID = 0
	} else {
		o.ID = attrID(bd.order.Uint64(bd.buf[r.f.sampleIDOffset:]))
	}
	o.EventAttr = r.getAttr(o.ID, false)
	if o.EventAttr == nil {
		return nil
	}

	// Decode hdr.Misc
	o.CPUMode = CPUMode(hdr.Misc & recordMiscCPUModeMask)
	o.ExactIP = (hdr.Misc&recordMiscExactIP != 0)

	// Decode the rest of the sample
	t := o.EventAttr.SampleFormat
	o.Format = t
	bd.u64If(t&SampleFormatIdentifier != 0)
	o.IP = bd.u64If(t&SampleFormatIP != 0)
	o.PID = int(bd.i32If(t&SampleFormatTID != 0))
	o.TID = int(bd.i32If(t&SampleFormatTID != 0))
	o.Time = bd.u64If(t&SampleFormatTime != 0)
	o.Addr = bd.u64If(t&SampleFormatAddr != 0)
	bd.u64If(t&SampleFormatID != 0)
	o.StreamID = bd.u64If(t&SampleFormatStreamID != 0)
	o.CPU = bd.u32If(t&SampleFormatCPU != 0)
	o.Res = bd.u32If(t&SampleFormatCPU != 0)
	o.Period = bd.u64If(t&SampleFormatPeriod != 0)

	if t&SampleFormatRead != 0 {
		r.parseReadFormat(bd, o.EventAttr.ReadFormat, &o.SampleRead)
	}

	if t&SampleFormatCallchain != 0 {
		callchainLen := int(bd.u64())
		if o.Callchain == nil || cap(o.Callchain) < callchainLen {
			o.Callchain = make([]uint64, callchainLen)
		} else {
			o.Callchain = o.Callchain[:callchainLen]
		}
		bd.u64s(o.Callchain)
	} else {
		o.Callchain = nil
	}

	if t&SampleFormatRaw != 0 {
		rawSize := int(bd.u32())
		if o.Raw == nil || cap(o.Raw) < rawSize {
			o.Raw = make([]byte, rawSize)
		} else {
			o.Raw = o.Raw[:rawSize]
		}
		bd.bytes(o.Raw)
	} else {
		o.Raw = nil
	}

	o.BranchHWIndex = bd.i64If(o.EventAttr.BranchSampleType&BranchSampleHWIndex != 0)

	if t&SampleFormatBranchStack != 0 {
		count := int(bd.u64())
		if o.BranchStack == nil || cap(o.BranchStack) < count {
			o.BranchStack = make([]BranchRecord, count)
		} else {
			o.BranchStack = o.BranchStack[:count]
		}
		for i := range o.BranchStack {
			br := &o.BranchStack[i]
			br.From = bd.u64()
			br.To = bd.u64()
			flags := bd.u64()
			// First 4 bits are flags
			br.Flags = BranchFlags(flags & 0x0f)
			// Next 16 bits are cycles
			br.Cycles = uint16(flags >> 4)
			// Next 4 bits are type
			br.Type = BranchType((flags >> 20) & 0x0f)
		}
	}

	if t&SampleFormatRegsUser != 0 {
		o.RegsUserABI = SampleRegsABI(bd.u64())
		count := weight(o.EventAttr.SampleRegsUser)
		if o.RegsUser == nil || cap(o.RegsUser) < count {
			o.RegsUser = make([]uint64, count)
		} else {
			o.RegsUser = o.RegsUser[:count]
		}
		if o.RegsUserABI == SampleRegsABINone {
			o.RegsUser = o.RegsUser[:0:0]
		} else {
			bd.u64s(o.RegsUser)
		}
	}

	if t&SampleFormatStackUser != 0 {
		size := int(bd.u64())
		if o.StackUser == nil || cap(o.StackUser) < size {
			o.StackUser = make([]byte, size)
		} else {
			o.StackUser = o.StackUser[:size]
		}
		bd.bytes(o.StackUser)
		o.StackUserDynSize = bd.u64()
	} else {
		o.StackUser = nil
		o.StackUserDynSize = 0
	}

	if t&SampleFormatWeight != 0 {
		o.Weight = bd.u64()
		o.Weights = Weights{}
	} else if t&SampleFormatWeightStruct != 0 {
		// N.B. the kernel memcpys the 64-bit union value regardless of
		// format, so on big endian systems the fields appear in the
		// opposite order. Read as a 64-bit value and extract the
		// fields to handle both little and big endian.
		weight := bd.u64()
		o.Weights.Var1 = uint32(weight)
		o.Weights.Var2 = uint16(weight >> 32)
		o.Weights.Var3 = uint16(weight >> 48)
		// For ease of use, also put Var1 in Weight. If you
		// only care about one weight, that's the one.
		o.Weight = uint64(o.Weights.Var1)
	}

	if t&SampleFormatDataSrc != 0 {
		o.DataSrc = decodeDataSrc(bd.u64())
	}

	transaction := bd.u64If(t&SampleFormatTransaction != 0)
	o.Transaction = Transaction(transaction & 0xffffffff)
	o.AbortCode = uint32(transaction >> 32)

	if t&SampleFormatRegsIntr != 0 {
		o.RegsIntrABI = SampleRegsABI(bd.u64())
		count := weight(o.EventAttr.SampleRegsIntr)
		if o.RegsIntr == nil || cap(o.RegsIntr) < count {
			o.RegsIntr = make([]uint64, count)
		} else {
			o.RegsIntr = o.RegsIntr[:count]
		}
		if o.RegsIntrABI == SampleRegsABINone {
			o.RegsIntr = o.RegsIntr[:0:0]
		} else {
			bd.u64s(o.RegsIntr)
		}
	}

	if t&SampleFormatPhysAddr != 0 {
		o.PhysAddr = bd.u64()
	}

	if t&SampleFormatCGroup != 0 {
		o.CGroup = bd.u64()
	}

	if t&SampleFormatDataPageSize != 0 {
		o.DataPageSize = bd.u64()
	}

	if t&SampleFormatCodePageSize != 0 {
		o.CodePageSize = bd.u64()
	}

	if t&SampleFormatAux != 0 {
		auxLen := int(bd.u64())
		if o.Aux == nil || cap(o.Aux) < auxLen {
			o.Aux = make([]byte, auxLen)
		} else {
			o.Aux = o.Aux[:auxLen]
		}
		bd.bytes(o.Aux)
	} else {
		o.Aux = nil
	}

	return o
}

func (r *Records) parseReadFormat(bd *bufDecoder, f ReadFormat, out *[]Count) {
	n := 1
	if f&ReadFormatGroup != 0 {
		n = int(bd.u64())
	}

	if *out == nil || cap(*out) < n {
		*out = make([]Count, n)
	} else {
		*out = (*out)[:n]
	}

	if f&ReadFormatGroup == 0 {
		o := &(*out)[0]
		o.Value = bd.u64()
		o.TimeEnabled = bd.u64If(f&ReadFormatTotalTimeEnabled != 0)
		o.TimeRunning = bd.u64If(f&ReadFormatTotalTimeRunning != 0)
		if f&ReadFormatID != 0 {
			o.EventAttr = r.getAttr(attrID(bd.u64()), false)
		} else {
			o.EventAttr = nil
		}
	} else {
		for i := range *out {
			o := &(*out)[i]
			o.TimeEnabled = bd.u64If(f&ReadFormatTotalTimeEnabled != 0)
			o.TimeRunning = bd.u64If(f&ReadFormatTotalTimeRunning != 0)
			o.Value = bd.u64()
			if f&ReadFormatID != 0 {
				o.EventAttr = r.getAttr(attrID(bd.u64()), false)
			} else {
				o.EventAttr = nil
			}
		}
	}
}

func decodeDataSrc(d uint64) (out DataSrc) {
	// See perf_mem_data_src in include/uapi/linux/perf_event.h
	op := (d >> 0) & 0x1f
	lvl := (d >> 5) & 0x3fff
	snoop := (d >> 19) & 0x1f
	lock := (d >> 24) & 0x3
	dtlb := (d >> 26) & 0x7f
	levelNum := (d >> 33) & 0xf
	remote := (d >> 37) & 0x1
	snoopX := (d >> 38) & 0x3 // two bit extension of snoop
	blk := (d >> 40) & 0x7
	hops := (d >> 43) & 0x7

	if op&0x1 != 0 {
		out.Op = DataSrcOpNA
	} else {
		out.Op = DataSrcOp(op >> 1)
	}

	if lvl&0x1 != 0 {
		out.Miss, out.Level = false, DataSrcLevelNA
	} else {
		out.Miss = (lvl & 0x4) != 0
		out.Level = DataSrcLevel(lvl >> 3)
	}

	if snoop&0x1 != 0 {
		out.Snoop = DataSrcSnoopNA
	} else {
		out.Snoop = DataSrcSnoop(snoop >> 1)
		if snoopX&0x1 != 0 {
			out.Snoop |= DataSrcSnoopFwd
		}
	}

	if lock&0x1 != 0 {
		out.Locked = DataSrcLockNA
	} else if lock&0x02 != 0 {
		out.Locked = DataSrcLockLocked
	} else {
		out.Locked = DataSrcLockUnlocked
	}

	if dtlb&0x1 != 0 {
		out.TLB = DataSrcTLBNA
	} else {
		out.TLB = DataSrcTLB(dtlb >> 1)
	}

	out.LevelNum = DataSrcLevelNum(levelNum)
	out.Remote = remote != 0

	if blk&0x1 != 0 {
		out.Block = DataSrcBlockNA
	} else {
		out.Block = DataSrcBlock(blk >> 1)
	}

	out.Hops = DataSrcHops(hops)

	return
}

func weight(x uint64) int {
	x -= (x >> 1) & 0x5555555555555555
	x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
	x = (x + (x >> 4)) & 0x0f0f0f0f0f0f0f0f
	return int((x * 0x0101010101010101) >> 56)
}
