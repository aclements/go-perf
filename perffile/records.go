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
//
// Typical usage is
//    rs := file.Records()
//    for rs.Next() {
//      switch r := rs.Record.(type) {
//        ...
//      }
//    }
//    if rs.Err() { ... }
type Records struct {
	f   *File
	sr  *bufferedSectionReader // or *io.SectionReader
	err error

	// The current record.  Determine which type of record this is
	// using a type switch.
	Record Record

	// Read buffer.  Reused (and resized) by Next.
	buf []byte

	// Cache for common record types
	recordMmap   RecordMmap
	recordComm   RecordComm
	recordExit   RecordExit
	recordFork   RecordFork
	recordSample RecordSample
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
		r.parseCommon(bd, &common)
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
		r.Record = r.parseSample(bd, &hdr)

	case recordTypeMmap2:
		r.Record = r.parseMmap(bd, &hdr, &common, true)
	}
	if r.err != nil {
		return false
	}
	return true
}

func (r *Records) getAttr(id attrID) *EventAttr {
	if attr, ok := r.f.idToAttr[id]; ok {
		return attr
	}
	r.err = fmt.Errorf("event has unknown eventAttr ID %d", id)
	return nil
}

// parseCommon parses the common sample_id structure in the trailer of
// non-sample records.
func (r *Records) parseCommon(bd *bufDecoder, o *RecordCommon) bool {
	// Get EventAttr ID
	if r.f.recordIDOffset == -1 {
		o.ID = 0
	} else {
		o.ID = attrID(bd.order.Uint64(bd.buf[len(bd.buf)+r.f.recordIDOffset:]))
	}
	o.EventAttr = r.getAttr(o.ID)
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

	// Decode fields
	o.PID, o.TID = int(bd.i32()), int(bd.i32())
	o.Addr, o.Len, o.PgOff = bd.u64(), bd.u64(), bd.u64()
	if v2 {
		o.Major, o.Minor = bd.u32(), bd.u32()
		o.Ino, o.InoGeneration = bd.u64(), bd.u64()
		o.Prot, o.Flags = bd.u32(), bd.u32()
	}
	o.Filename = bd.cstring()

	return o
}

func (r *Records) parseLost(bd *bufDecoder, hdr *recordHeader, common *RecordCommon) Record {
	o := &RecordLost{RecordCommon: *common}
	o.Format |= SampleFormatID

	o.ID = attrID(bd.u64())
	o.EventAttr = r.getAttr(o.ID)
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
		o.EventAttr = r.getAttr(id)
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

func (r *Records) parseSample(bd *bufDecoder, hdr *recordHeader) Record {
	o := &r.recordSample

	// Get sample EventAttr ID
	if r.f.sampleIDOffset == -1 {
		o.ID = 0
	} else {
		o.ID = attrID(bd.order.Uint64(bd.buf[r.f.sampleIDOffset:]))
	}
	o.EventAttr = r.getAttr(o.ID)
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

	rawSize := bd.u32If(t&SampleFormatRaw != 0)
	bd.skip(int(rawSize))

	if t&SampleFormatBranchStack != 0 {
		count := int(bd.u64())
		if o.BranchStack == nil || cap(o.BranchStack) < count {
			o.BranchStack = make([]BranchRecord, count)
		} else {
			o.BranchStack = o.BranchStack[:count]
		}
		for i := range o.BranchStack {
			o.BranchStack[i].From = bd.u64()
			o.BranchStack[i].To = bd.u64()
			o.BranchStack[i].Flags = bd.u64()
		}
	}

	if t&SampleFormatRegsUser != 0 {
		o.RegsABI = SampleRegsABI(bd.u64())
		count := weight(o.EventAttr.SampleRegsUser)
		if o.Regs == nil || cap(o.Regs) < count {
			o.Regs = make([]uint64, count)
		} else {
			o.Regs = o.Regs[:count]
		}
		bd.u64s(o.Regs)
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

	o.Weight = bd.u64If(t&SampleFormatWeight != 0)

	if t&SampleFormatDataSrc != 0 {
		o.DataSrc = decodeDataSrc(bd.u64())
	}

	transaction := bd.u64If(t&SampleFormatTransaction != 0)
	o.Transaction = Transaction(transaction & 0xffffffff)
	o.AbortCode = uint32(transaction >> 32)

	return o
}

func (r *Records) parseReadFormat(bd *bufDecoder, f ReadFormat, out *[]SampleRead) {
	n := 1
	if f&ReadFormatGroup != 0 {
		n = int(bd.u64())
	}

	if *out == nil || cap(*out) < n {
		*out = make([]SampleRead, n)
	} else {
		*out = (*out)[:n]
	}

	if f&ReadFormatGroup == 0 {
		o := &(*out)[0]
		o.Value = bd.u64()
		o.TimeEnabled = bd.u64If(f&ReadFormatTotalTimeEnabled != 0)
		o.TimeRunning = bd.u64If(f&ReadFormatTotalTimeRunning != 0)
		if f&ReadFormatID != 0 {
			o.EventAttr = r.getAttr(attrID(bd.u64()))
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
				o.EventAttr = r.getAttr(attrID(bd.u64()))
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
	return
}

func weight(x uint64) int {
	x -= (x >> 1) & 0x5555555555555555
	x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
	x = (x + (x >> 4)) & 0x0f0f0f0f0f0f0f0f
	return int((x * 0x0101010101010101) >> 56)
}
