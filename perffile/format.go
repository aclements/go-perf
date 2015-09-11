// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"fmt"
	"io"
)

const numFeatureBits = 256

// perf_file_header from tools/perf/util/header.h
type fileHeader struct {
	Magic    [8]byte
	Size     uint64      // Size of fileHeader on disk
	AttrSize uint64      // Size of fileAttr on disk
	Attrs    fileSection // Array of fileAttr
	Data     fileSection // Alternating recordHeader and record
	_        fileSection // event_types; ignored in v2

	Features [numFeatureBits / 64]uint64 // Bitmap of feature
}

func (h *fileHeader) hasFeature(f feature) bool {
	return h.Features[f/64]&(1<<(uint(f)%64)) != 0
}

// perf_file_section from tools/perf/util/header.h
type fileSection struct {
	Offset, Size uint64
}

func (s fileSection) sectionReader(r io.ReaderAt) *io.SectionReader {
	return io.NewSectionReader(r, int64(s.Offset), int64(s.Size))
}

func (s fileSection) data(r io.ReaderAt) ([]byte, error) {
	out := make([]byte, s.Size)
	n, err := r.ReadAt(out, int64(s.Offset))
	if n == len(out) {
		return out, nil
	}
	return nil, err
}

// HEADER_* enum from tools/perf/util/header.h
type feature int

const (
	featureReserved feature = iota // always cleared
	featureTracingData
	featureBuildID

	featureHostname
	featureOSRelease
	featureVersion
	featureArch
	featureNrCpus
	featureCPUDesc
	featureCPUID
	featureTotalMem
	featureCmdline
	featureEventDesc
	featureCPUTopology
	featureNUMATopology
	featureBranchStack
	featurePMUMappings
	featureGroupDesc
)

// perf_file_attr from tools/perf/util/header.c
type fileAttr struct {
	Attr EventAttr
	IDs  fileSection // array of attrID, one per core/thread
}

// eventAttrV0 is on-disk version 0 of the perf_event_attr structure.
// Later versions extended this with additional fields, but the header
// is always the same.
type eventAttrV0 struct {
	Type                    EventType
	Size                    uint32
	Config                  uint64
	SamplePeriodOrFreq      uint64
	SampleFormat            SampleFormat
	ReadFormat              ReadFormat
	Flags                   EventFlags
	WakeupEventsOrWatermark uint32
	BPType                  uint32
	BPAddrOrConfig1         uint64
}

// eventAttrVN is the on-disk latest version of the perf_event_attr
// structure (currently version 4).
type eventAttrVN struct {
	eventAttrV0

	// ABI v1
	BPLenOrConfig2 uint64

	// ABI v2
	BranchSampleType uint64

	// ABI v3
	SampleRegsUser  uint64
	SampleStackUser uint32
	Pad1            uint32 // Align to uint64

	// ABI v4
	SampleRegsIntr uint64
}

// TODO: Make public
type attrID uint64

// EventAttr describes an event that is recorded in a perf.data file.
//
// This corresponds to the perf_event_attr struct from
// include/uapi/linux/perf_event.h
type EventAttr struct {
	// Type specifies the major type of this event, such as
	// hardware event, software event, or tracepoint.
	Type EventType

	// Config gives Type-specific configuration information. In
	// perf_event_attr, this corresponds to the fields config,
	// config1, and config2.
	Config [3]uint64

	// SamplePeriod is the sampling period for this event. Either
	// this or SampleFreq will be non-zero, depending on
	// Flags&EventFlagsFreq.
	SamplePeriod uint64
	// SampleFreq is the sampling frequency of this event.
	SampleFreq uint64

	// The format of RecordSamples
	SampleFormat SampleFormat

	// The format of SampleRead
	ReadFormat ReadFormat

	Flags EventFlags

	// WakeupEvents specifies to wake up every WakeupEvents
	// events. Either this or WakeupWatermark will be non-zero,
	// depending on Flags&EventFlagWakeupWatermark.
	WakeupEvents uint32
	// WakeupWatermark specifies to wake up every WakeupWatermark
	// bytes.
	WakeupWatermark uint32

	BPType uint32
	BPAddr uint64
	BPLen  uint64

	BranchSampleType uint64 // TODO: PERF_SAMPLE_BRANCH_*

	// SampleRegsUser is a bitmask of user-space registers
	// captured at each sample in RecordSample.RegsUser. The
	// hardware register corresponding to each bit depends on the
	// register ABI.
	SampleRegsUser uint64

	// Size of user stack to dump on samples
	SampleStackUser uint32

	// SampleRegsIntr is a bitmask of registers captured at each
	// sample in RecordSample.RegsIntr. If precise == 0, these
	// registers are captured at the PMU interrupt. If precise >
	// 0, these registers are captured by the hardware at when it
	// samples an instruction.
	SampleRegsIntr uint64
}

// An EventType is a general class of perf event.
//
// This corresponds to the perf_type_id enum from
// include/uapi/linux/perf_event.h
type EventType uint32

//go:generate stringer -type=EventType

const (
	EventTypeHardware EventType = iota
	EventTypeSoftware
	EventTypeTracepoint
	EventTypeHWCache
	EventTypeRaw
	EventTypeBreakpoint
)

// A SampleFormat is a bitmask of the fields recorded by a sample.
//
// This corresponds to the perf_event_sample_format enum from
// include/uapi/linux/perf_event.h
type SampleFormat uint64

const (
	SampleFormatIP SampleFormat = 1 << iota
	SampleFormatTID
	SampleFormatTime
	SampleFormatAddr
	SampleFormatRead
	SampleFormatCallchain
	SampleFormatID
	SampleFormatCPU
	SampleFormatPeriod
	SampleFormatStreamID
	SampleFormatRaw
	SampleFormatBranchStack
	SampleFormatRegsUser
	SampleFormatStackUser
	SampleFormatWeight
	SampleFormatDataSrc
	SampleFormatIdentifier
	SampleFormatTransaction
	SampleFormatRegsIntr
)

// sampleIDOffset returns the byte offset of the ID field within an
// on-disk sample record with this sample format. If there is no ID
// field, it returns -1.
func (s SampleFormat) sampleIDOffset() int {
	// See __perf_evsel__calc_id_pos in tools/perf/util/evsel.c.

	if s&SampleFormatIdentifier != 0 {
		return 0
	}
	if s&SampleFormatID == 0 {
		return -1
	}

	off := 0
	if s&SampleFormatIP != 0 {
		off += 8
	}
	if s&SampleFormatTID != 0 {
		off += 8
	}
	if s&SampleFormatTime != 0 {
		off += 8
	}
	if s&SampleFormatAddr != 0 {
		off += 8
	}
	return off
}

// recordIDOffset returns the byte offset of the ID field of
// non-sample records relative to the end of the on-disk sample. If
// there is no ID field, it returns -1.
func (s SampleFormat) recordIDOffset() int {
	// See __perf_evsel__calc_is_pos in tools/perf/util/evsel.c.

	if s&SampleFormatIdentifier != 0 {
		return -8
	}
	if s&SampleFormatID == 0 {
		return -1
	}

	off := 0
	if s&SampleFormatCPU != 0 {
		off -= 8
	}
	if s&SampleFormatStreamID != 0 {
		off -= 8
	}
	return off - 8
}

// trailerBytes returns the length in the sample_id trailer for
// non-sample records.
func (s SampleFormat) trailerBytes() int {
	s &= SampleFormatTID | SampleFormatTime | SampleFormatID | SampleFormatStreamID | SampleFormatCPU | SampleFormatIdentifier
	return 8 * weight(uint64(s))
}

// ReadFormat is a bitmask of the fields recorded in the SampleRead
// field(s) of a sample.
//
// This corresponds to the perf_event_read_format enum from
// include/uapi/linux/perf_event.h
type ReadFormat uint64

const (
	ReadFormatTotalTimeEnabled = 1 << iota
	ReadFormatTotalTimeRunning
	ReadFormatID
	ReadFormatGroup
)

// EventFlags is a bitmask of boolean properties of an event.
//
// This corresponds to the perf_event_attr enum from
// include/uapi/linux/perf_event.h
type EventFlags uint64

const (
	// Event is disabled by default
	EventFlagDisabled EventFlags = 1 << iota
	// Children inherit this event
	EventFlagInherit
	// Event must always be on the PMU
	EventFlagPinned
	// Event is only group on PMU
	EventFlagExclusive
	// Don't count events in user/kernel/hypervisor/when idle
	EventFlagExcludeUser
	EventFlagExcludeKernel
	EventFlagExcludeHypervisor
	EventFlagExcludeIdle
	// Include mmap data
	EventFlagMmap
	// Include comm data
	EventFlagComm
	// Use frequency, not period
	EventFlagFreq
	// Per task counts
	EventFlagInheritStat
	// Next exec enables this event
	EventFlagEnableOnExec
	// Trace fork/exit
	EventFlagTask
	// WakeupWatermark is set rather than WakeupEvents.
	EventFlagWakeupWatermark

	// Skip two bits here for EventFlagPreciseIPMask

	// Non-exec mmap data
	EventFlagMmapData EventFlags = 1 << (2 + iota)
	// All events have SampleField fields
	EventFlagSampleIDAll
	// Don't count events in host/guest
	EventFlagExcludeHost
	EventFlagExcludeGuest
	// Don't include kernel/user callchains
	EventFlagExcludeCallchainKernel
	EventFlagExcludeCallchainUser
	// Include inode data in mmap events
	EventFlagMmapInodeData
	// Flag comm events that are due to an exec
	EventFlagCommExec
)

const (
	// TODO: Pull precise IP out; it's not a flag
	EventFlagPreciseArbitrarySkid EventFlags = iota << 15
	EventFlagPreciseConstantSkid
	EventFlagPreciseTryZeroSkid
	EventFlagPreciseZeroSkip
	EventFlagPreciseIPMask EventFlags = 0x3 << 15
)

// perf_event_header from include/uapi/linux/perf_event.h
type recordHeader struct {
	Type RecordType
	Misc recordMisc
	Size uint16
}

// A RecordType indicates the type of a record in a profile. A record
// can either be a profiling sample or give information about changes
// to system state, such as a process calling mmap.
type RecordType uint32

//go:generate stringer -type=RecordType

const (
	RecordTypeMmap RecordType = 1 + iota
	RecordTypeLost
	RecordTypeComm
	RecordTypeExit
	RecordTypeThrottle
	RecordTypeUnthrottle
	RecordTypeFork
	RecordTypeRead
	RecordTypeSample
	recordTypeMmap2 // internal extended RecordTypeMmap

	recordTypeUserStart RecordType = 64
)

// perf_user_event_type in tools/perf/util/event.h
//
// TODO: Figure out what to do with these. Some of these are only to
// direct parsing so they should never escape the API. Some of these
// are only for perf.data pipes.
const (
	recordTypeHeaderAttr      RecordType = recordTypeUserStart + iota
	recordTypeHeaderEventType            // deprecated
	recordTypeHeaderTracingData
	recordTypeHeaderBuildID
	recordTypeHeaderFinishedRound
	recordTypeHeaderIDIndex
)

// PERF_RECORD_MISC_* from include/uapi/linux/perf_event.h
type recordMisc uint16

const (
	recordMiscCPUModeMask recordMisc = 7
	recordMiscMmapData               = 1 << 13
	recordMiscCommExec               = 1 << 13
	recordMiscExactIP                = 1 << 14
)

// Record is the common interface implemented by all profile record
// types.
type Record interface {
	Type() RecordType
	Common() *RecordCommon
}

// RecordCommon stores fields that are common to all record types, as
// well as additional metadata. It is not itself a Record.
//
// Many fields are optional and their presence is determined by the
// bitmask EventAttr.SampleFormat. Some record types guarantee that
// some of these fields will be filled.
type RecordCommon struct {
	// Offset is the byte offset of this event in the perf.data
	// file.
	Offset int64

	// Format is a bit mask of SampleFormat* values that indicate
	// which optional fields of this record are valid.
	Format SampleFormat

	// EventAttr is the event, if any, associated with this record.
	EventAttr *EventAttr

	PID, TID int    // if SampleFormatTID
	Time     uint64 // if SampleFormatTime
	ID       attrID // if SampleFormatID or SampleFormatIdentifier
	StreamID uint64 // if SampleFormatStreamID
	CPU, Res uint32 // if SampleFormatCPU
}

func (r *RecordCommon) Common() *RecordCommon {
	return r
}

// A RecordUnknown is a Record of unknown or unimplemented type.
type RecordUnknown struct {
	recordHeader

	RecordCommon

	Data []byte
}

func (r *RecordUnknown) Type() RecordType {
	return RecordType(r.recordHeader.Type)
}

// A RecordMmap records when a process being profiled called mmap.
// RecordMmaps can also occur at the beginning of a profile to
// describe the existing memory layout.
type RecordMmap struct {
	// RecordCommon.PID and .TID will always be filled
	RecordCommon

	Data bool // from header.misc

	// Addr and Len are the virtual address of the start of this
	// mapping and its length in bytes.
	Addr, Len uint64
	// FileOffset is the byte offset in the mapped file of the
	// beginning of this mapping.
	FileOffset         uint64
	Major, Minor       uint32
	Ino, InoGeneration uint64
	Prot, Flags        uint32
	Filename           string
}

func (r *RecordMmap) Type() RecordType {
	return RecordTypeMmap
}

// A RecordLost records that profiling events were lost because of a
// buffer overflow.
type RecordLost struct {
	// RecordCommon.ID and .EventAttr will always be filled
	RecordCommon

	NumLost uint64
}

func (r *RecordLost) Type() RecordType {
	return RecordTypeLost
}

// A RecordComm records that a process being profiled called exec.
// RecordComms can also occur at the beginning of a profile to
// describe the existing set of processes.
type RecordComm struct {
	// RecordCommon.PID and .TID will always be filled
	RecordCommon

	Exec bool // from header.misc

	Comm string
}

func (r *RecordComm) Type() RecordType {
	return RecordTypeComm
}

// A RecordExit records that a process or thread exited.
type RecordExit struct {
	// RecordCommon.PID, .TID, and .Time will always be filled
	RecordCommon

	PPID, PTID int
}

func (r *RecordExit) Type() RecordType {
	return RecordTypeExit
}

// A RecordThrottle records that interrupt throttling was enabled or
// disabled.
type RecordThrottle struct {
	// RecordCommon.Time, .ID, and .StreamID, and .EventAttr will
	// always be filled
	RecordCommon

	Enable bool
}

func (r *RecordThrottle) Type() RecordType {
	return RecordTypeThrottle
}

// A RecordFork records that a process called clone to either fork the
// process or create a new thread.
type RecordFork struct {
	// RecordCommon.PID, .TID, and .Time will always be filled
	RecordCommon

	PPID, PTID int
}

func (r *RecordFork) Type() RecordType {
	return RecordTypeFork
}

// A RecordSample records a profiling sample event.
//
// Typically only a subset of the fields are used. Which fields are
// set can be determined from the bitmask
// RecordSample.EventAttr.SampleFormat.
type RecordSample struct {
	// RecordCommon.EventAttr will always be filled.
	// RecordCommon.Format descibes the optional fields in this
	// structure, as well as the optional common fields.
	RecordCommon

	CPUMode CPUMode // from header.misc
	ExactIP bool    // from header.misc

	IP     uint64 // if SampleFormatIP
	Addr   uint64 // if SampleFormatAddr
	Period uint64 // if SampleFormatPeriod

	// SampleRead records raw event counter values. If this is an
	// event group, this slice will have more than one element;
	// otherwise, it will have one element.
	SampleRead []SampleRead // if SampleFormatRead

	// Callchain gives the call stack of the sampled instruction,
	// starting from the sampled instruction itself. The call
	// chain may span several types of stacks (e.g., it may start
	// in a kernel stack, then transition to a user stack). Before
	// the first IP from each stack there will be a Callchain*
	// constant indicating the stack type for the following IPs.
	Callchain []uint64 // if SampleFormatCallchain

	BranchStack []BranchRecord // if SampleFormatBranchStack

	// RegsUserABI and RegsUser record the ABI and values of
	// user-space registers as of this sample. Note that these are
	// the current user-space registers even if this sample
	// occurred at a kernel PC. RegsUser[i] records the value of
	// the register indicated by the i-th set bit of
	// EventAttr.SampleRegsUser.
	RegsUserABI SampleRegsABI // if SampleFormatRegsUser
	RegsUser    []uint64      // if SampleFormatRegsUser

	// RegsIntrABI And RegsIntr record the ABI and values of
	// registers as of this sample. Unlike RegsUser, these can be
	// kernel-space registers if this sample occurs in the kernel.
	// RegsIntr[i] records the value of the register indicated by
	// the i-th set bit of EventAttr.SampleRegsIntr.
	RegsIntrABI SampleRegsABI // if SampleFormatRegsIntr
	RegsIntr    []uint64      // if SampleFormatRegsIntr

	StackUser        []byte // if SampleFormatStackUser
	StackUserDynSize uint64 // if SampleFormatStackUser

	Weight  uint64  // if SampleFormatWeight
	DataSrc DataSrc // if SampleFormatDataSrc

	Transaction Transaction // if SampleFormatTransaction
	AbortCode   uint32      // if SampleFormatTransaction
}

func (r *RecordSample) Type() RecordType {
	return RecordTypeSample
}

func (r *RecordSample) String() string {
	// TODO: Stringers for other record types
	f := r.Format
	s := fmt.Sprintf("{Offset:%#x Format:%#x EventAttr:%p CPUMode:%v ExactIP:%v", r.Offset, r.Format, r.EventAttr, r.CPUMode, r.ExactIP)
	if f&(SampleFormatID|SampleFormatIdentifier) != 0 {
		s += fmt.Sprintf(" ID:%d", r.ID)
	}
	if f&SampleFormatIP != 0 {
		s += fmt.Sprintf(" IP:%#x", r.IP)
	}
	if f&SampleFormatTID != 0 {
		s += fmt.Sprintf(" PID:%d TID:%d", r.PID, r.TID)
	}
	if f&SampleFormatTime != 0 {
		s += fmt.Sprintf(" Time:%d", r.Time)
	}
	if f&SampleFormatAddr != 0 {
		s += fmt.Sprintf(" Addr:%#x", r.Addr)
	}
	if f&SampleFormatStreamID != 0 {
		s += fmt.Sprintf(" StreamID:%d", r.StreamID)
	}
	if f&SampleFormatCPU != 0 {
		s += fmt.Sprintf(" CPU:%d Res:%d", r.CPU, r.Res)
	}
	if f&SampleFormatPeriod != 0 {
		s += fmt.Sprintf(" Period:%d", r.Period)
	}
	if f&SampleFormatRead != 0 {
		s += fmt.Sprintf(" SampleRead:%v", r.SampleRead)
	}
	if f&SampleFormatCallchain != 0 {
		s += fmt.Sprintf(" Callchain:%#x", r.Callchain)
	}
	if f&SampleFormatBranchStack != 0 {
		s += fmt.Sprintf(" BranchStack:%v", r.BranchStack)
	}
	if f&SampleFormatRegsUser != 0 {
		s += fmt.Sprintf(" RegsUserABI:%v RegsUser:%v", r.RegsUserABI, r.RegsUser)
	}
	if f&SampleFormatRegsIntr != 0 {
		s += fmt.Sprintf(" RegsIntrABI:%v RegsIntr:%v", r.RegsIntrABI, r.RegsIntr)
	}
	if f&SampleFormatStackUser != 0 {
		s += fmt.Sprintf(" StackUser:[...] StackUserDynSize:%d", r.StackUserDynSize)
	}
	if f&SampleFormatWeight != 0 {
		s += fmt.Sprintf(" Weight:%d", r.Weight)
	}
	if f&SampleFormatDataSrc != 0 {
		s += fmt.Sprintf(" DataSrc:%+v", r.DataSrc)
	}
	if f&SampleFormatTransaction != 0 {
		s += fmt.Sprintf(" Transaction:%v AbortCode:%d", r.Transaction, r.AbortCode)
	}
	return s + "}"
}

// A CPUMode indicates the privilege level of a sample or event.
//
// This corresponds to PERF_RECORD_MISC_CPUMODE from
// include/uapi/linux/perf_event.h
type CPUMode uint16

//go:generate stringer -type=CPUMode

const (
	CPUModeUnknown CPUMode = iota
	CPUModeKernel
	CPUModeUser
	CPUModeHypervisor
	CPUModeGuestKernel
	CPUModeGuestUser
)

// A SampleRead records the raw value of an event counter.
//
// Typically only a subset of the fields are used. Which fields are
// set can be determined from the bitmask in the sample's
// EventAttr.ReadFormat.
//
// This corresponds to perf_event_read_format from
// include/uapi/linux/perf_event.h
type SampleRead struct {
	Value       uint64     // Event counter value
	TimeEnabled uint64     // if ReadFormatTotalTimeEnabled
	TimeRunning uint64     // if ReadFormatTotalTimeRunning
	EventAttr   *EventAttr // if ReadFormatID
}

// A BranchRecord records a single branching event in a sample.
type BranchRecord struct {
	From, To uint64
	Flags    uint64 // TODO: Flags encoding
}

// Special markers used in RecordSample.Callchain to mark boundaries
// between types of stacks.
//
// These correspond to PERF_CONTEXT_* from
// include/uapi/linux/perf_event.h
const (
	CallchainHypervisor  = 0xffffffffffffffe0 // -32
	CallchainKernel      = 0xffffffffffffff80 // -128
	CallchainUser        = 0xfffffffffffffe00 // -512
	CallchainGuest       = 0xfffffffffffff800 // -2048
	CallchainGuestKernel = 0xfffffffffffff780 // -2176
	CallchainGuestUser   = 0xfffffffffffff600 // -2560
)

// SampleRegsABI indicates the register ABI of a given sample for
// architectures that support multiple ABIs.
//
// This corresponds to the perf_sample_regs_abi enum from
// include/uapi/linux/perf_event.h
type SampleRegsABI uint64

//go:generate stringer -type=SampleRegsABI

const (
	SampleRegsABINone SampleRegsABI = iota
	SampleRegsABI32
	SampleRegsABI64
)

type DataSrc struct {
	Op     DataSrcOp
	Miss   bool // if true, Level specifies miss, rather than hit
	Level  DataSrcLevel
	Snoop  DataSrcSnoop
	Locked DataSrcLock
	TLB    DataSrcTLB
}

type DataSrcOp int

const (
	DataSrcOpLoad DataSrcOp = 1 << iota
	DataSrcOpStore
	DataSrcOpPrefetch
	DataSrcOpExec

	DataSrcOpNA DataSrcOp = 0
)

func (i DataSrcOp) String() string {
	// TODO: It would be nice if stringer could do this.
	s := ""
	if i&DataSrcOpLoad != 0 {
		s += "Load|"
	}
	if i&DataSrcOpStore != 0 {
		s += "Store|"
	}
	if i&DataSrcOpPrefetch != 0 {
		s += "Prefetch|"
	}
	if i&DataSrcOpExec != 0 {
		s += "Exec|"
	}
	if len(s) == 0 {
		return "NA"
	}
	return s[:len(s)-1]
}

type DataSrcLevel int

const (
	DataSrcLevelL1  DataSrcLevel = 1 << iota
	DataSrcLevelLFB              // Line fill buffer
	DataSrcLevelL2
	DataSrcLevelL3
	DataSrcLevelLocalRAM     // Local DRAM
	DataSrcLevelRemoteRAM1   // Remote DRAM (1 hop)
	DataSrcLevelRemoteRAM2   // Remote DRAM (2 hops)
	DataSrcLevelRemoteCache1 // Remote cache (1 hop)
	DataSrcLevelRemoteCache2 // Remote cache (2 hops)
	DataSrcLevelIO           // I/O memory
	DataSrcLevelUncached

	DataSrcLevelNA DataSrcLevel = 0
)

func (i DataSrcLevel) String() string {
	s := ""
	if i&DataSrcLevelL1 != 0 {
		s += "L1|"
	}
	if i&DataSrcLevelLFB != 0 {
		s += "LFB|"
	}
	if i&DataSrcLevelL2 != 0 {
		s += "L2|"
	}
	if i&DataSrcLevelL3 != 0 {
		s += "L3|"
	}
	if i&DataSrcLevelLocalRAM != 0 {
		s += "LocalRAM|"
	}
	if i&DataSrcLevelRemoteRAM1 != 0 {
		s += "RemoteRAM1|"
	}
	if i&DataSrcLevelRemoteRAM2 != 0 {
		s += "RemoteRAM2|"
	}
	if i&DataSrcLevelRemoteCache1 != 0 {
		s += "RemoteCache1|"
	}
	if i&DataSrcLevelRemoteCache2 != 0 {
		s += "RemoteCache2|"
	}
	if i&DataSrcLevelIO != 0 {
		s += "IO|"
	}
	if i&DataSrcLevelUncached != 0 {
		s += "Uncached|"
	}
	if len(s) == 0 {
		return "NA"
	}
	return s[:len(s)-1]
}

type DataSrcSnoop int

const (
	DataSrcSnoopNone DataSrcSnoop = 1 << iota
	DataSrcSnoopHit
	DataSrcSnoopMiss
	DataSrcSnoopHitM // Snoop hit modified

	DataSrcSnoopNA DataSrcSnoop = 0
)

func (i DataSrcSnoop) String() string {
	s := ""
	if i&DataSrcSnoopNone != 0 {
		s += "None|"
	}
	if i&DataSrcSnoopHit != 0 {
		s += "Hit|"
	}
	if i&DataSrcSnoopMiss != 0 {
		s += "Miss|"
	}
	if i&DataSrcSnoopHitM != 0 {
		s += "HitM|"
	}
	if len(s) == 0 {
		return "NA"
	}
	return s[:len(s)-1]
}

type DataSrcLock int

//go:generate stringer -type=DataSrcLock

const (
	DataSrcLockNA DataSrcLock = iota
	DataSrcLockUnlocked
	DataSrcLockLocked
)

type DataSrcTLB int

const (
	DataSrcTLBHit DataSrcTLB = 1 << iota
	DataSrcTLBMiss
	DataSrcTLBL1
	DataSrcTLBL2
	DataSrcTLBHardwareWalker
	DataSrcTLBOSFaultHandler

	DataSrcTLBNA DataSrcTLB = 0
)

func (i DataSrcTLB) String() string {
	s := ""
	if i&DataSrcTLBHit != 0 {
		s += "Hit|"
	}
	if i&DataSrcTLBMiss != 0 {
		s += "Miss|"
	}
	if i&DataSrcTLBL1 != 0 {
		s += "L1|"
	}
	if i&DataSrcTLBL2 != 0 {
		s += "L2|"
	}
	if i&DataSrcTLBHardwareWalker != 0 {
		s += "HardwareWalker|"
	}
	if i&DataSrcTLBOSFaultHandler != 0 {
		s += "OSFaultHandler|"
	}
	if len(s) == 0 {
		return "NA"
	}
	return s[:len(s)-1]
}

type Transaction int

const (
	TransactionElision       Transaction = 1 << iota // From elision
	TransactionTransaction                           // From transaction
	TransactionSync                                  // Instruction is related
	TransactionAsync                                 // Instruction is not related
	TransactionRetry                                 // Retry possible
	TransactionConflict                              // Conflict abort
	TransactionCapacityWrite                         // Capactiy write abort
	TransactionCapacityRead                          // Capactiy read abort
)
