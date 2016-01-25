#!/usr/bin/python3

import os
import sys
import subprocess
import operator

# TODO: Improve the quality of computed metrics using event groups.

def gather(counters):
    counters = list(frozenset(counters))
    r, w = os.pipe()
    args = ["ocperf.py", "stat", "-x;", "--log-fd", str(w)]
    for counter in counters:
        args.extend(["-e", counter])
    args.extend(sys.argv[1:])
    perf = subprocess.Popen(args, pass_fds=[w])
    os.close(w)
    r = os.fdopen(r)
    output = r.read()
    r.close()
    res = perf.wait()
    if res:
        sys.exit(res)

    #print(output, file=sys.stderr) # Debugging

    # Parse output. Annoyingly, column 3 is the names, but ocperf
    # generated names trim off modifiers, so we can't distinguish
    # cmasks and such. Hence, we just match them up by order.
    cmap = {}
    for line in output.splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        row = line.split(";")

        # TODO: What's column 2? Column 3 is name. Column 4 is run
        # time of the counter. Column 5 is fraction of time counter
        # was running.
        cmap[counters[len(cmap)]] = int(row[0])

    return cmap

class Formula:
    def __init__(self, children):
        self.children = children

    def eval(self, ctx):
        raise NotImplementedError("eval is abstract")

    def events(self):
        raise NotImplementedError("events is abstract")

    def __add__(self, o):
        return FormulaOp(operator.add, self, o)

    def __radd__(self, o):
        return FormulaOp(operator.add, o, self)

    def __sub__(self, o):
        return FormulaOp(operator.sub, self, o)

    def __rsub__(self, o):
        return FormulaOp(operator.sub, o, self)

    def __mul__(self, o):
        return FormulaOp(operator.mul, self, o)

    def __rmul__(self, o):
        return FormulaOp(operator.mul, o, self)

    def __truediv__(self, o):
        return FormulaOp(operator.truediv, self, o)

    def __rtruediv__(self, o):
        return FormulaOp(operator.truediv, o, self)

class FormulaOp(Formula):
    def __init__(self, op, *args):
        super().__init__(args)
        self.op = op

    def eval(self, ctx):
        args = [c.eval(ctx) if isinstance(c, Formula) else c
                for c in self.children]
        return self.op(*args)

    def events(self):
        events = []
        for child in self.children:
            if isinstance(child, Formula):
                events.extend(child.events())
        return events

class E(Formula):
    def __init__(self, event):
        super().__init__([])
        self.event = event

    def eval(self, ctx):
        return ctx[self.event]

    def events(self):
        return [self.event]

# Top-Down bottleneck formulas. These are mostly from Yasin 2014, "A
# Top-Down Method for Performance Analysis and Counters Architecture"
# with some more detailed metrics from pmu-tools' toplev.py.

clocks = E("CPU_CLK_UNHALTED.THREAD")
issueWidth = 4
slots = issueWidth * clocks

frontendBound = E("IDQ_UOPS_NOT_DELIVERED.CORE") / slots
fetchLatencyBound = E("IDQ_UOPS_NOT_DELIVERED.CORE:cmask=4") / clocks
fetchBandwidthBound = frontendBound - fetchLatencyBound

badSpeculation = (E("UOPS_ISSUED.ANY") - E("UOPS_RETIRED.RETIRE_SLOTS") + 4 * E("INT_MISC.RECOVERY_CYCLES")) / slots

retiring = E("UOPS_RETIRED.RETIRE_SLOTS") / slots

memoryBound = (E("CYCLE_ACTIVITY.STALLS_MEM_ANY") + E("RESOURCE_STALLS.SB")) / clocks
numExecutionStalls = (E("CYCLE_ACTIVITY.CYCLES_NO_EXECUTE") - E("RS_EVENTS.EMPTY_CYCLES") + E("UOPS_EXECUTED.THREAD:cmask=1") - E("UOPS_EXECUTED.THREAD:cmask=2")) / clocks
coreBound = numExecutionStalls - memoryBound

# See https://software.intel.com/en-us/node/544440 for detailed
# explanations of L1 bound reasons.
l1Bound = (E("CYCLE_ACTIVITY.STALLS_MEM_ANY") - E("CYCLE_ACTIVITY.STALLS_L1D_MISS")) / clocks
l1_STLBHitCost = 7
l1_DTLBMiss = (l1_STLBHitCost * E("DTLB_LOAD_MISSES.STLB_HIT") + E("DTLB_LOAD_MISSES.WALK_DURATION")) / clocks
l1_SFBCost = 13
l1_StoreForwardBlocked = l1_SFBCost * E("LD_BLOCKS.STORE_FORWARD") / clocks
l1_LockStoreFraction = E("MEM_UOPS_RETIRED.LOCK_LOADS") / E("MEM_UOPS_RETIRED.ALL_STORES")
oroDemandRFOC1 = FormulaOp(min, E("CPU_CLK_UNHALTED.THREAD"), E("OFFCORE_REQUESTS_OUTSTANDING.CYCLES_WITH_DEMAND_RFO"))
l1_LockLatency = l1_LockStoreFraction * oroDemandRFOC1 / clocks
# Load spans two cache lines.
l1_SplitLoads = 13 * E("LD_BLOCKS.NO_SR") / clocks
# Earlier load conflicts with later store on bottom 12 bits.
l1_4KAliasCost = 7
l1_4KAliasing = (l1_4KAliasCost * E("LD_BLOCKS_PARTIAL.ADDRESS_ALIAS")) / clocks
# L1D fill buffer limits further demand loads.
l1_LoadMissRealLatency = E("L1D_PEND_MISS.PENDING") / (E("MEM_LOAD_UOPS_RETIRED.L1_MISS") + E("MEM_LOAD_UOPS_RETIRED.HIT_LFB"))
l1_FBFull = l1_LoadMissRealLatency * E("L1D_PEND_MISS.FB_FULL:cmask=1") / clocks

l2Bound = (E("CYCLE_ACTIVITY.STALLS_L1D_MISS") - E("CYCLE_ACTIVITY.STALLS_L2_MISS")) / clocks
# toplev.py gives the following alternate formula, but the results
# don't seem any different.
#l2Bound = (E("CYCLE_ACTIVITY.STALLS_L1D_PENDING") - E("CYCLE_ACTIVITY.STALLS_L2_PENDING")) / clocks
l3HitFraction = E("MEM_LOAD_UOPS_RETIRED.LLC_HIT") / (E("MEM_LOAD_UOPS_RETIRED.LLC_HIT") + 7*E("MEM_LOAD_UOPS_RETIRED.LLC_MISS"))
l3Bound = l3HitFraction * E("CYCLE_ACTIVITY.STALLS_L2_MISS") / clocks
extMemoryBound = (1 - l3HitFraction) * E("CYCLE_ACTIVITY.STALLS_L2_MISS") / clocks

# These formulas come from toplev.py. They're very different from the
# Yasin paper, but are based on the same idea, and don't depend on
# undocumented uncore counters.
extMem_BandwidthBound = E("OFFCORE_REQUESTS_OUTSTANDING.DEMAND_DATA_RD:cmask=6") / clocks
extMem_LatencyBound = (E("OFFCORE_REQUESTS_OUTSTANDING.CYCLES_WITH_DEMAND_DATA_RD") - E("OFFCORE_REQUESTS_OUTSTANDING.DEMAND_DATA_RD:cmask=6")) / clocks

class Node:
    def __init__(self, label, value, *children):
        self.label, self.value, self.children = label, value, children

    def events(self):
        counters = []
        if self.value is not None:
            counters.extend(self.value.events())
        for child in self.children:
            counters.extend(child.events())
        return counters

    def eval(self, ctx):
        if self.value is None:
            return sum(child.eval(ctx) for child in self.children)
        return self.value.eval(ctx)

    def show(self, ctx, indent=0):
        value = self.eval(ctx)
        print("%-30s %6.2f%%" % ("  " * indent + self.label, 100*value))
        for child in self.children:
            child.show(ctx, indent+1)

tree = Node("All slots", None,
            Node("µop issued", None,
                 Node("Retires", retiring),
                 Node("Bad speculation", badSpeculation)),
            Node("No µop issued", None,
                 Node("Front end bound", frontendBound,
                      Node("Fetch latency bound", fetchLatencyBound),
                      Node("Fetch bandwidth bound", fetchBandwidthBound)),
                 Node("Back end bound", None,
                      Node("Core bound", coreBound),
                      Node("Memory bound", memoryBound,
                           Node("L1 bound", l1Bound,
                                Node("DTLB load miss", l1_DTLBMiss),
                                Node("Load blocked by store forwarding", l1_StoreForwardBlocked),
                                Node("Lock latency", l1_LockLatency),
                                Node("Split loads", l1_SplitLoads),
                                Node("4K aliasing", l1_4KAliasing),
                                Node("Fill buffer full", l1_FBFull)),
                           Node("L2 bound", l2Bound),
                           Node("L3 bound", l3Bound),
                           Node("Ext mem bound", extMemoryBound,
                                Node("Bandwidth", extMem_BandwidthBound),
                                Node("Latency", extMem_LatencyBound))))))

def cpu_family_model():
    family = model = None
    for l in open("/proc/cpuinfo").readlines():
        if l.startswith("cpu family\t"):
            family = int(l.split(":")[1])
        if l.startswith("model\t"):
            model = int(l.split(":")[1])
        if family != None and model != None:
            return family, model
    print("failed to get CPU family and model from /proc/cpuinfo",
          file=sys.stderr)
    sys.exit(1)

def main():
    # Only Ivy Bridge for now.
    family, model = cpu_family_model()
    if (family, model) not in [(0x06, 0x3a), (0x06, 0x3e)]:
        print("unsupported CPU model %02x_%02xH" % (family, model),
              file=sys.stderr)
        sys.exit(1)

    events = tree.events()
    ctx = gather(events)
    tree.show(ctx)

if __name__ == "__main__":
    main()
