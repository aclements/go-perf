package perffile

/*gendefs:C
#include <include/uapi/linux/perf_event.h>
*/

//go:generate stringer -type=EventHardware,EventSoftware,HWCache,HWCacheOp,HWCacheResult
//go:generate go run ../cmd/bitstringer/main.go -type=BreakpointOp -strip=BreakpointOp

// EventGeneric is a generic representation of a performance event.
//
// Any perf event can be represented by EventGeneric, but some
// encoding is generally necessary. Hence, EventGeneric can be
// translated back and forth between specific Event* types.
type EventGeneric struct {
	// Type specifies the major type of this event, such as
	// hardware event, software event, or tracepoint.
	Type EventType

	// ID is the specific event within the class described by
	// Type.
	//
	// In perf_event_attr, this corresponds to either
	// perf_event_attr.config or, if Type == EventTypeBreakpoint,
	// perf_event_attr.bp_type.
	ID uint64

	// Config gives additional configuration specific to the event
	// described by Type and ID.
	//
	// In perf_event_attr, this corresponds to
	// perf_event_attr.config1 and config2.
	Config []uint64
}

// Decode decodes a generic event g into a specific event type.
func (g *EventGeneric) Decode() Event {
	switch g.Type {
	case EventTypeHardware:
		return EventHardware(g.ID)

	case EventTypeSoftware:
		return EventSoftware(g.ID)

	case EventTypeTracepoint:
		return EventTracepoint(g.ID)

	case EventTypeHWCache:
		return EventHWCache{
			HWCache(g.ID),
			HWCacheOp(g.ID >> 8),
			HWCacheResult(g.ID >> 16),
		}

	case EventTypeRaw:
		return EventRaw(g.ID)

	case EventTypeBreakpoint:
		return EventBreakpoint{
			BreakpointOp(g.ID),
			g.Config[0],
			g.Config[1],
		}
	}

	return eventUnknown{*g}
}

type eventUnknown struct {
	g EventGeneric
}

func (e eventUnknown) Generic() EventGeneric {
	return e.g
}

// EventHardware represents a hardware event.
//
// This corresponds to the perf_hw_id enum from
// include/uapi/linux/perf_event.h
type EventHardware uint64

//gendefs perf_hw_id.PERF_COUNT_HW_* EventHardware -omit-max

const (
	EventHardwareCPUCycles EventHardware = iota
	EventHardwareInstructions
	EventHardwareCacheReferences
	EventHardwareCacheMisses
	EventHardwareBranchInstructions
	EventHardwareBranchMisses
	EventHardwareBusCycles
	EventHardwareStalledCyclesFrontend
	EventHardwareStalledCyclesBackend
	EventHardwareRefCPUCycles
)

func (e EventHardware) Generic() EventGeneric {
	return EventGeneric{Type: EventTypeHardware, ID: uint64(e)}
}

// EventSoftware represents a software event.
//
// This corresponds to the perf_sw_ids enum from
// include/uapi/linux/perf_event.h
type EventSoftware uint64

//gendefs perf_sw_ids.PERF_COUNT_SW_* EventSoftware -omit-max

const (
	EventSoftwareCPUClock EventSoftware = iota
	EventSoftwareTaskClock
	EventSoftwarePageFaults
	EventSoftwareContextSwitches
	EventSoftwareCPUMigrations
	EventSoftwarePageFaultsMin
	EventSoftwarePageFaultsMaj
	EventSoftwareAlignmentFaults
	EventSoftwareEmulationFaults
	EventSoftwareDummy
	EventSoftwareBpfOutput
)

func (e EventSoftware) Generic() EventGeneric {
	return EventGeneric{Type: EventTypeSoftware, ID: uint64(e)}
}

// EventTracepoint represents a kernel tracepoint event.
//
// The IDs of the tracepoint events are given by the
// tracing/events/*/*/id files under debugfs.
type EventTracepoint uint64

func (e EventTracepoint) Generic() EventGeneric {
	return EventGeneric{Type: EventTypeTracepoint, ID: uint64(e)}
}

// EventHWCache represents a hardware cache event.
type EventHWCache struct {
	Level  HWCache
	Op     HWCacheOp
	Result HWCacheResult
}

func (e EventHWCache) Generic() EventGeneric {
	id := uint64(e.Level) | uint64(e.Op)<<8 | uint64(e.Result)<<16
	return EventGeneric{Type: EventTypeHWCache, ID: id}
}

// HWCache represents a level in the hardware cache.
//
// This corresponds to the perf_hw_cache_id enum from
// include/uapi/linux/perf_event.h
type HWCache uint8

//gendefs perf_hw_cache_id.PERF_COUNT_HW_CACHE_* HWCache -omit-max

const (
	HWCacheL1D HWCache = iota
	HWCacheL1I
	HWCacheLL
	HWCacheDTLB
	HWCacheITLB
	HWCacheBPU
	HWCacheNode
)

// HWCacheOp represents a type of access to a hardware cache.
//
// This corresponds to the perf_hw_cache_op_id enum from
// include/uapi/linux/perf_event.h
type HWCacheOp uint8

//gendefs perf_hw_cache_op_id.PERF_COUNT_HW_CACHE_OP_* HWCacheOp -omit-max

const (
	HWCacheOpRead HWCacheOp = iota
	HWCacheOpWrite
	HWCacheOpPrefetch
)

// HWCacheResult represents the result of a accessing a hardware
// cache.
//
// This corresponds to the perf_hw_cache_op_result_id enum from
// include/uapi/linux/perf_event.h
type HWCacheResult uint8

//gendefs perf_hw_cache_op_result_id.PERF_COUNT_HW_CACHE_RESULT_* HWCacheResult -omit-max

const (
	HWCacheResultAccess HWCacheResult = iota
	HWCacheResultMiss
)

// EventRaw represents a "raw" hardware PMU event in a CPU-specific
// format.
type EventRaw uint64

func (e EventRaw) Generic() EventGeneric {
	return EventGeneric{Type: EventTypeRaw, ID: uint64(e)}
}

// EventBreakpoint represents a breakpoint event.
//
// Breakpoint events are triggered by a specific type of access to an
// address in memory.
type EventBreakpoint struct {
	// Op specifies what type of access to Addr should trigger
	// this event.
	Op BreakpointOp
	// Addr is the address to watch for operation Op.
	Addr uint64
	// Len is the number of bytes to watch at Addr. What sizes are
	// supported depends on the hardware, but it generally must be
	// a small power of 2.
	Len uint64
}

func (e EventBreakpoint) Generic() EventGeneric {
	return EventGeneric{Type: EventTypeBreakpoint, ID: uint64(e.Op), Config: []uint64{e.Addr, e.Len}}
}

// BreakpointOp is a type of memory access that can trigger a
// breakpoint event.
//
// This corresponds to the HW_BREAKPOINT_* constants from
// include/uapi/linux/hw_breakpoint.h
type BreakpointOp uint32

const (
	BreakpointOpR  BreakpointOp = 1
	BreakpointOpW               = 2
	BreakpointOpRW              = BreakpointOpR | BreakpointOpW
	BreakpointOpX               = 4
)
