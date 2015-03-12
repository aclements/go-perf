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

// TODO: Make public
type attrID uint64

// TODO: Consider separating on-disk perf_event_attr structure from
// structure exposed to user (or deserialize it manually).

// EventAttr describes an event that is recorded in a perf.data file.
//
// perf_event_attr from include/uapi/linux/perf_event.h
type EventAttr struct {
	// Major type: hardware/software/tracepoint/etc
	Type EventClass

	// Size of EventAttr structure on disk
	Size uint32

	// Type-specific configuration information
	Config uint64

	SamplePeriodOrFreq uint64

	// The format of RecordSamples
	SampleFormat SampleFormat

	// The format of SampleRead
	ReadFormat ReadFormat

	Flags EventFlags

	WakeupEventsOrWatermark uint32

	BPType           uint32
	BPAddrOrConfig1  uint64
	BPLenOrConfig2   uint64 // Note: Only if size >= 72
	BranchSampleType uint64 // Note: Only if size >= 80, TODO: PERF_SAMPLE_BRANCH_*

	// Note: From here down only if size >= 96

	// The set of user regs to dump on samples
	SampleRegsUser uint64

	// Size of user stack to dump on samples
	SampleStackUser uint32

	// Align to uint64
	_ uint32
}

// perf_type_id from include/uapi/linux/perf_event.h
type EventClass uint32

//go:generate stringer -type=EventClass
const (
	EventClassHardware EventClass = iota
	EventClassSoftware
	EventClassTracepoint
	EventClassHWCache
	EventClassRaw
	EventClassBreakpoint
)

// perf_event_sample_format from include/uapi/linux/perf_event.h
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
)

// sampleIDOffset returns the on-disk byte offset of the ID field of
// sample records with this sample format.
func (s SampleFormat) sampleIDOffset() int {
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

// recordIDOffset returns the on-disk byte offset of the ID field of
// non-sample records relative to the end of the sample.
func (s SampleFormat) recordIDOffset() int {
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

// perf_event_read_format from include/uapi/linux/perf_event.h
type ReadFormat uint64

const (
	ReadFormatTotalTimeEnabled = 1 << iota
	ReadFormatTotalTimeRunning
	ReadFormatID
	ReadFormatGroup
)

// Bitmask in perf_event_attr from include/uapi/linux/perf_event.h
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
	// WakeupEventsOrWatermark is watermark
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
const (
	RecordTypeHeaderAttr      RecordType = recordTypeUserStart + iota
	recordTypeHeaderEventType            // deprecated
	RecordTypeHeaderTracingData
	RecordTypeHeaderBuildID
	RecordTypeHeaderFinishedRound
	RecordTypeHeaderIDIndex
)

// PERF_RECORD_MISC_* from include/uapi/linux/perf_event.h
type recordMisc uint16

const (
	recordMiscCPUModeMask recordMisc = 7
	recordMiscMmapData               = 1 << 13
	recordMiscCommExec               = 1 << 13
	recordMiscExactIP                = 1 << 14
)

type Record interface {
	Type() RecordType
}

// RecordCommon stores fields that are common to all record types, as
// well as additional metadata.
//
// Many fields are optional and their presence is determined by the
// sample format in the perf.data file.  Some record types guarantee
// that some of these fields will be filled.
type RecordCommon struct {
	// Byte offset of this event in the perf.data file
	Offset int64

	// Format is a bit mask of SampleFormat* values that indicate
	// which optional fields of this record are valid.
	Format SampleFormat

	// The event, if any, associated with this record.
	EventAttr *EventAttr

	PID, TID int    // if SampleFormatTID
	Time     uint64 // if SampleFormatTime
	ID       attrID // if SampleFormatID or SampleFormatIdentifier
	StreamID uint64 // if SampleFormatStreamID
	CPU, Res uint32 // if SampleFormatCPU
}

// Placeholder for unknown or unimplemented record types
type RecordUnknown struct {
	recordHeader

	RecordCommon

	Data []byte
}

func (r *RecordUnknown) Type() RecordType {
	return RecordType(r.recordHeader.Type)
}

type RecordMmap struct {
	// RecordCommon.PID and .TID will always be filled
	RecordCommon

	Data bool // from header.misc

	Addr, Len, PgOff   uint64
	Major, Minor       uint32
	Ino, InoGeneration uint64
	Prot, Flags        uint32
	Filename           string
}

func (r *RecordMmap) Type() RecordType {
	return RecordTypeMmap
}

type RecordLost struct {
	// RecordCommon.ID and .EventAttr will always be filled
	RecordCommon

	NumLost uint64
}

func (r *RecordLost) Type() RecordType {
	return RecordTypeLost
}

type RecordComm struct {
	// RecordCommon.PID and .TID will always be filled
	RecordCommon

	Exec bool // from header.misc

	Comm string
}

func (r *RecordComm) Type() RecordType {
	return RecordTypeComm
}

type RecordExit struct {
	// RecordCommon.PID, .TID, and .Time will always be filled
	RecordCommon

	PPID, PTID int
}

func (r *RecordExit) Type() RecordType {
	return RecordTypeExit
}

type RecordThrottle struct {
	// RecordCommon.Time, .ID, and .StreamID, and .EventAttr will
	// always be filled
	RecordCommon

	Enable bool
}

func (r *RecordThrottle) Type() RecordType {
	return RecordTypeThrottle
}

type RecordFork struct {
	// RecordCommon.PID, .TID, and .Time will always be filled
	RecordCommon

	PPID, PTID int
}

func (r *RecordFork) Type() RecordType {
	return RecordTypeFork
}

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

	// Raw event counter values.  If this is an event group, this
	// slice will have more than one element; otherwise, it will
	// have one element.
	SampleRead []SampleRead // if SampleFormatRead

	// Callchain gives the call stack of the sampled instruction,
	// starting from the sampled instruction itself. The call
	// chain may span several types of stacks (e.g., it may start
	// in a kernel stack, then transition to a user stack). Before
	// the first IP from each stack there will be a Callchain*
	// constant indicating the stack type for the following IPs.
	Callchain []uint64 // if SampleFormatCallchain

	BranchStack []BranchRecord // if SampleFormatBranchStack

	RegsABI SampleRegsABI // if SampleFormatRegsUser
	Regs    []uint64      // if SampleFormatRegsUser

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
		s += fmt.Sprintf(" RegsABI:%v Regs:%v", r.RegsABI, r.Regs)
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

type CPUMode uint16

// PERF_RECORD_MISC_CPUMODE from include/uapi/linux/perf_event.h
//go:generate stringer -type=CPUMode
const (
	CPUModeUnknown CPUMode = iota
	CPUModeKernel
	CPUModeUser
	CPUModeHypervisor
	CPUModeGuestKernel
	CPUModeGuestUser
)

// See perf_event_read_format in include/uapi/linux/perf_event.h
type SampleRead struct {
	Value       uint64     // Event counter value
	TimeEnabled uint64     // if ReadFormatTotalTimeEnabled
	TimeRunning uint64     // if ReadFormatTotalTimeRunning
	EventAttr   *EventAttr // if ReadFormatID
}

type BranchRecord struct {
	From, To uint64
	Flags    uint64 // TODO: Flags encoding
}

// Special markers used in RecordSample.Callchain to mark boundaries
// between types of stacks.
//
// PERF_CONTEXT_* from include/uapi/linux/perf_event.h
const (
	CallchainHypervisor  = 0xffffffffffffffe0 // -32
	CallchainKernel      = 0xffffffffffffff80 // -128
	CallchainUser        = 0xfffffffffffffe00 // -512
	CallchainGuest       = 0xfffffffffffff800 // -2048
	CallchainGuestKernel = 0xfffffffffffff780 // -2176
	CallchainGuestUser   = 0xfffffffffffff600 // -2560
)

// perf_sample_regs_abi from include/uapi/linux/perf_event.h
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

type DataSrcSnoop int

const (
	DataSrcSnoopNone DataSrcSnoop = 1 << iota
	DataSrcSnoopHit
	DataSrcSnoopMiss
	DataSrcSnoopHitM // Snoop hit modified

	DataSrcSnoopNA DataSrcSnoop = 0
)

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
