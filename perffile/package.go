// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package perffile is a parser for Linux perf.data profiles.
//
// Parsing a perf.data profile starts with a call to New or Open to
// open a perf.data file. A perf.data file consists of a sequence of
// records, which can be retrieved with File.Records, as well as
// several metadata fields, which can be retrieved with other methods
// of File.
package perffile // import "github.com/aclements/go-perf/perffile"
