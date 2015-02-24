// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
)

type File struct {
	r      io.ReaderAt
	closer io.Closer
	hdr    fileHeader

	attrs    []fileAttr
	idToAttr map[attrID]*EventAttr

	featureSections map[feature]fileSection

	idOffset int // byte offset of AttrID in sample
}

// New reads a "perf.data" file from r.
//
// The caller must keep r open as long as it is using the returned
// *File.
func New(r io.ReaderAt) (*File, error) {
	file := &File{r: r}

	// See perf_session__read_header in tools/perf/util/header.c

	// Read header
	//
	// TODO: Support big endian
	sr := io.NewSectionReader(r, 0, 1024)
	if err := binary.Read(sr, binary.LittleEndian, &file.hdr); err != nil {
		return nil, err
	}
	if string(file.hdr.Magic[:]) != "PERFILE2" {
		return nil, fmt.Errorf("bad or unsupported file magic %+v", string(file.hdr.Magic[:]))
	}
	if file.hdr.Size != uint64(binary.Size(&file.hdr)) {
		return nil, fmt.Errorf("bad header size %d", file.hdr.Size)
	}
	// TODO: perf supports attrSize 64, 72, 80, and 96 for compatibility
	if file.hdr.AttrSize != uint64(binary.Size(&fileAttr{})) {
		return nil, fmt.Errorf("bad attr size %d", file.hdr.AttrSize)
	}

	// hdr.Data.Size is the last thing written out by perf, so if
	// it's zero, we're working with a partial file.
	if file.hdr.Data.Size == 0 {
		return nil, fmt.Errorf("truncated data file")
	}

	// Read EventAttrs
	if err := readSlice(file.hdr.Attrs.sectionReader(r), &file.attrs); err != nil {
		return nil, err
	}
	// Check EventAttr sizes
	attrSize := uint32(binary.Size(&EventAttr{}))
	for _, attr := range file.attrs {
		if attr.Attr.Size != attrSize {
			return nil, fmt.Errorf("bad attr size %d", attr.Attr.Size)
		}
	}

	// Read EventAttr IDs and create ID -> EventAttr map
	file.idToAttr = make(map[attrID]*EventAttr)
	for _, attr := range file.attrs {
		var ids []attrID
		if err := readSlice(attr.IDs.sectionReader(r), &ids); err != nil {
			return nil, err
		}
		for _, id := range ids {
			file.idToAttr[id] = &attr.Attr
		}
	}

	// If there's just one event, samples may implicitly refer to
	// that event, in which case there may be no IDs.  Create a
	// synthetic ID of 0.
	if len(file.idToAttr) == 0 {
		if len(file.attrs) > 1 {
			return nil, fmt.Errorf("file has multiple EventAttrs, but no IDs")
		}
		if file.attrs[0].Attr.SampleFormat&(SampleFormatID|SampleFormatIdentifier) != 0 {
			return nil, fmt.Errorf("sample format has IDs, but events don't have IDs")
		}
		file.idToAttr[0] = &file.attrs[0].Attr
		file.idOffset = -1
	} else {
		// Compute offset of sample AttrID field
		file.idOffset = -1
		for _, attr := range file.attrs {
			x := attr.Attr.SampleFormat.idOffset()
			if x == -1 {
				return nil, fmt.Errorf("events have no ID field")
			} else if file.idOffset == -1 {
				file.idOffset = x
			} else if file.idOffset != x {
				return nil, fmt.Errorf("events have incompatible ID offsets %d and %d", file.idOffset, x)
			}
		}
	}

	// Load feature section information
	sr = io.NewSectionReader(r, int64(file.hdr.Data.Offset+file.hdr.Data.Size), int64(numFeatureBits*binary.Size(fileSection{})))
	file.featureSections = make(map[feature]fileSection)
	for bit := feature(0); bit < feature(numFeatureBits); bit++ {
		if !file.hdr.hasFeature(bit) {
			continue
		}
		sec := fileSection{}
		if err := binary.Read(sr, binary.LittleEndian, &sec); err != nil {
			return nil, err
		}
		file.featureSections[bit] = sec
	}

	return file, nil
}

// Open opens the named "perf.data" file using os.Open.
//
// The caller must call f.Close() on the returned file when it is
// done.
func Open(name string) (*File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	ff, err := New(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	ff.closer = f
	return ff, nil
}

// Clsoe closes the File.
//
// If the File was created using New directly instead of Open, Close
// has no effect.
func (f *File) Close() error {
	var err error
	if f.closer != nil {
		err = f.closer.Close()
		f.closer = nil
	}
	return err
}

// readSlice reads an entire section into a slice.  v must be a
// pointer to a slice; the slice itself may be nil.  The section size
// must be an exact multiple of the size of the element type of v.
func readSlice(sr *io.SectionReader, v interface{}) error {
	// Figure out slice value size
	vt := reflect.TypeOf(v)
	if vt.Kind() != reflect.Ptr || vt.Elem().Kind() != reflect.Slice {
		panic("v must be a pointer to a slice")
	}
	et := vt.Elem().Elem()
	esize := binary.Size(reflect.Zero(et).Interface())
	nelem := int(sr.Size() / int64(esize))
	if sr.Size()%int64(esize) != 0 {
		return fmt.Errorf("section size %d is not a multiple of element size %d", sr.Size(), esize)
	}

	// Create slice
	reflect.ValueOf(v).Elem().Set(reflect.MakeSlice(vt.Elem(), nelem, nelem))

	// Read in to slice
	return binary.Read(sr, binary.LittleEndian, v)
}

func (f *File) stringFeature(bit feature) (string, error) {
	sec, ok := f.featureSections[bit]
	if !ok {
		return "", nil
	}
	data, err := sec.data(f.r)
	if err != nil {
		return "", err
	}
	bd := bufDecoder{data, binary.LittleEndian}
	bd.u32() // Ignore length; string is \0-terminated
	return bd.cstring(), nil
}

// TODO: featureBuildID

// Hostname returns the hostname of the machine that recorded this
// profile, or "" if unknown.
func (f *File) Hostname() (string, error) {
	return f.stringFeature(featureHostname)
}

// OSRelease returns the OS release of the machine that recorded this
// profile, or "" if unknown.
func (f *File) OSRelease() (string, error) {
	return f.stringFeature(featureOSRelease)
}

// Version returns the perf version that recorded this profile, or ""
// if unknown.
func (f *File) Version() (string, error) {
	return f.stringFeature(featureVersion)
}

// Arch returns the host architecture of the machine that recorded
// this profile, or "" if unknown.
func (f *File) Arch() (string, error) {
	return f.stringFeature(featureArch)
}

// TODO: featureNrCpus

// CPUDesc returns a string describing the CPU of the machine that
// recorded this profile, or "" if unknown.
func (f *File) CPUDesc() (string, error) {
	return f.stringFeature(featureCPUDesc)
}

// CPUID returns the CPUID string of the machine that recorded this
// profile, or "" if unknown.
func (f *File) CPUID() (string, error) {
	return f.stringFeature(featureCPUID)
}

// TODO: featureTotalMem

// CmdLine returns the list of command line arguments perf was invoked
// with.  If unknown, it returns nil, nil.
func (f *File) CmdLine() ([]string, error) {
	sec, ok := f.featureSections[featureCmdline]
	if !ok {
		return nil, nil
	}
	data, err := sec.data(f.r)
	if err != nil {
		return nil, err
	}
	bd := bufDecoder{data, binary.LittleEndian}
	out := make([]string, int(bd.u32()))
	for i := range out {
		part := make([]byte, int(bd.u32()))
		bd.bytes(part)
		out[i] = (&bufDecoder{part, nil}).cstring()
	}
	return out, nil
}

// TODO: featureEventDesc, featureCPUTopology, featureNUMATopology,
// featurePMUMappings, featureGroupDesc

func (f *File) Records() *Records {
	return &Records{f: f, sr: f.hdr.Data.sectionReader(f.r)}
}
