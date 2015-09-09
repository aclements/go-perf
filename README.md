go-perf is a set of tools for working with Linux perf.data profiles,
as well as a set of Go packages for parsing and interpreting such
profiles.

memlat
------

memlat is a web-based interactive browser for memory load latency
profiles. Such profiles give deep and detailed insight in to the
sources of memory stalls and conflicts, but are difficult to interpret
using traditional profiling tools. See the
[detailed documentation on godoc](http://godoc.org/github.com/aclements/go-perf/cmd/memlat).

There is also a predecessor of memlat in `cmd/memheat`. This tool
generates static SVG files summarizing memory load latency
distributions by function and source line. This may be removed in the
future.

dump
----

dump prints the detailed decoded contents of a perf.data profile. It's
similar to `perf report -D`, but is somewhat less mysterious. It's
particularly useful when developing with the perffile library because
it prints everything in terms of perffile structures.

Libraries
---------

This repository also contains two Go packages for parsing and
interpreting perf.data files.

[perffile](http://godoc.org/github.com/aclements/go-perf/perffile)
provides a parser for perf.data files. It can interpret all current
record types and almost all metadata fields.

[perfsession](http://godoc.org/github.com/aclements/go-perf/perfsession)
provides utilities for tracking session state while processing a
perf.data file. Its API is still evolving and should be considered
unstable.
