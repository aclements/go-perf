#!/usr/bin/python3

import os
import sys
import subprocess
import operator

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

    events = ["OFFCORE_REQUESTS_OUTSTANDING.DEMAND_DATA_RD:cmask=%d" % cmask
              for cmask in range(1, 18)]
    ctx = gather(["CPU_CLK_UNHALTED.THREAD"] + events)

    # Print PDF
    clocks = ctx["CPU_CLK_UNHALTED.THREAD"]
    for i, (ev1, ev2) in enumerate(zip(events, events[1:])):
        print(i + 1, 100 * (ctx[ev1] - ctx[ev2]) / clocks)

    if ctx[events[-1]] != 0:
        print("Warning: Highest cmask has non-zero event count %d" % ctx[events[-1]],
              file=sys.stderr)

if __name__ == "__main__":
    main()
