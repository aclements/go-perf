goperf is a collection of Go packages for parsing and making use of
Linux perf.data profiles.

Libraries
* perffile is a parser for perf.data files.
* perfsession provides utilities for tracking system state while
  processing a perf.data file.

Tools
* cmd/dump is a simple tool that reads a perf.data file and prints its
  raw records.
* cmd/memheat processes a perf.data file recorded by "perf mem record"
  and produces an SVG of the memory latency distributions on every
  instruction and source line.
