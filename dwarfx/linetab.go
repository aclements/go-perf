// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dwarfx

import (
	"debug/dwarf"
	"encoding/binary"
	"fmt"
	"path"
)

type LineReader struct {
	buf buf

	version              uint16
	minInstructionLength int
	lineBase             int
	lineRange            int
	opcodeBase           int
	opcodeLengths        []int
	directories          []string
	fileEntries          []*FileEntry

	state LineEntry
}

type FileEntry struct {
	FileName string
	Mtime    uint64 // Modification time, or 0 if unknown
	Length   int    // File length, or 0 if unknown
}

type LineEntry struct {
	Address     uint64 // program counter value
	FileIndex   int    // index of file name in file table
	FileEntry   *FileEntry
	Line        int  // source line number, beginning at 1.  0 if unknown.
	Column      int  // source column, beginning at 1.  0 if unknown.
	IsStmt      bool // this instruction begins a statement
	BasicBlock  bool // this instruction begins a basic block
	EndSequence bool // this is one past the last address in the table
}

type dwarf64Format struct{}

func (dwarf64Format) version() int {
	return 0
}

func (dwarf64Format) dwarf64() (bool, bool) {
	return true, true
}

func (dwarf64Format) addrsize() int {
	return 8
}

// NewLineReader returns a new reader for the line table of
// compilation unit cu.
//
// Line tables are per-compilation unit.  cu must be an Entry with tag
// TagCompileUnit.  line must be the contents of the .debug_line
// section of the corresponding ELF file.
//
// If this compilation unit has no line table, this returns nil, nil.
func NewLineReader(cu *dwarf.Entry, line []byte) (*LineReader, error) {
	off, ok := cu.Val(dwarf.AttrStmtList).(int64)
	if !ok {
		// cu has no line table
		return nil, nil
	}
	compDir, _ := cu.Val(dwarf.AttrCompDir).(string)

	if off > int64(len(line)) {
		off = int64(len(line))
	}

	// TODO: Use correct byte order and format.  The dwarf package
	// hides this information and it's annoying to dig out
	// ourselves.
	buf := makeBuf(nil, binary.LittleEndian, dwarf64Format{}, "line", dwarf.Offset(off), line[off:])

	// Compilation directory is implicitly directories[0]
	lr := &LineReader{buf: buf, directories: []string{compDir}}
	if err := lr.readPrologue(); err != nil {
		return nil, err
	}
	return lr, nil
}

// readPrologue reads the statement program prologue from r.buf.
func (r *LineReader) readPrologue() error {
	buf := &r.buf

	// [DWARF2 6.2.4]
	totalLength := dwarf.Offset(buf.uint32())
	if totalLength < dwarf.Offset(len(buf.data)) {
		buf.data = buf.data[:totalLength]
	}
	r.version = buf.uint16()
	prologueLength := dwarf.Offset(buf.uint32())
	programOffset := buf.off + prologueLength
	r.minInstructionLength = int(buf.uint8())
	defaultIsStmt := (buf.uint8() != 0)
	r.lineBase = int(int8(buf.uint8()))
	r.lineRange = int(buf.uint8())

	// Opcode length table
	r.opcodeBase = int(buf.uint8())
	r.opcodeLengths = make([]int, r.opcodeBase)
	for i := 1; i < r.opcodeBase; i++ {
		off := buf.off
		r.opcodeLengths[i] = int(buf.uint8())
		if buf.err != nil {
			return buf.err
		}
		knownLength, ok := knownOpcodeLengths[i]
		if ok && r.opcodeLengths[i] != knownLength {
			return DecodeError{"line", off, fmt.Sprintf("opcode %d expected to have length %d, but has length %d", i, knownLength, r.opcodeLengths[i])}
		}
	}

	// Include directories table.  The caller already set
	// directories[0] to the compilation directory.
	for {
		directory := buf.string()
		if buf.err != nil {
			return buf.err
		}
		if len(directory) == 0 {
			break
		}
		if !path.IsAbs(directory) {
			// Relative paths are implicitly relative to
			// the compilation directory.
			directory = path.Join(r.directories[0], directory)
		}
		r.directories = append(r.directories, directory)
	}

	// File name list.  File numbering starts with 1, so leave the
	// first entry nil.
	r.fileEntries = make([]*FileEntry, 1)
	for {
		if done, err := r.readFileEntry(); err != nil {
			return err
		} else if done {
			break
		}
	}

	// Skip to the beginning of the statement program
	buf.skip(int(programOffset - buf.off))

	// Create initial line program state
	r.state = LineEntry{Address: 0, FileIndex: 1, FileEntry: nil, Line: 1, Column: 0, IsStmt: defaultIsStmt, BasicBlock: false}
	r.updateFileEntry()

	return buf.err
}

// readFileEntry reads a file entry from either the prologue or a
// DW_LNE_define_file extended opcode and adds it to r.fileEntries.  A
// true return value indicates that there are no more entries to read.
func (r *LineReader) readFileEntry() (bool, error) {
	name := r.buf.string()
	if r.buf.err != nil {
		return false, r.buf.err
	}
	if len(name) == 0 {
		return true, nil
	}
	off := r.buf.off
	dirIndex := int(r.buf.uint())
	if !path.IsAbs(name) {
		if dirIndex >= len(r.directories) {
			return false, DecodeError{"line", off, "directory index too large"}
		}
		name = path.Join(r.directories[dirIndex], name)
	}
	mtime := r.buf.uint()
	length := int(r.buf.uint())

	r.fileEntries = append(r.fileEntries, &FileEntry{name, mtime, length})
	return false, nil
}

// updateFileEntry updates r.state.FileEntry after r.state.FileIndex
// has changed or r.fileEntries has changed.
func (r *LineReader) updateFileEntry() {
	if r.state.FileIndex < len(r.fileEntries) {
		r.state.FileEntry = r.fileEntries[r.state.FileIndex]
	} else {
		r.state.FileEntry = nil
	}
}

// Next reads the next row from the line table.  Rows are always in
// order of increasing Address, but Line may go forward or backward.
// It returns nil, nil when it reaches the end of the line table.  It
// returns an error if the data cannot be decoded as a line table.
func (r *LineReader) Next() (*LineEntry, error) {
	if r.buf.err != nil || r.state.EndSequence {
		return nil, r.buf.err
	}

	// Execute opcodes until we reach an opcode that emits a line
	// table entry
	for {
		if len(r.buf.data) == 0 {
			return nil, DecodeError{"line", r.buf.off, "line number table ended without a DW_LNE_end_sequence opcode"}
		}
		entry := r.step()
		if r.buf.err != nil {
			return nil, r.buf.err
		}
		if entry != nil {
			return entry, nil
		}
	}
}

// knownOpcodeLengths gives the opcode lengths (in varint arguments)
// of known standard opcodes.
var knownOpcodeLengths = map[int]int{
	lnsCopy:          0,
	lnsAdvancePC:     1,
	lnsAdvanceLine:   1,
	lnsSetFile:       1,
	lnsNegateStmt:    0,
	lnsSetBasicBlock: 0,
	lnsConstAddPC:    0,
	// lnsFixedAdvancePC takes a uint8 rather than a varint
}

// step processes the next opcode and updates r.state.  If the opcode
// emits a row in the line table, this returns the emitted row.
func (r *LineReader) step() *LineEntry {
	opcode := int(r.buf.uint8())

	if opcode >= r.opcodeBase {
		// Special opcode [DWARF2 6.2.5.1]
		adjustedOpcode := opcode - r.opcodeBase
		addressDelta := adjustedOpcode / r.lineRange
		lineDelta := r.lineBase + int(adjustedOpcode)%r.lineRange

		r.state.Line += lineDelta
		r.state.Address += uint64(r.minInstructionLength * addressDelta)

		goto emit
	}

	switch opcode {
	case 0:
		// Extended opcode [DWARF2 6.2.5.3]
		length := dwarf.Offset(r.buf.uint())
		startOff := r.buf.off
		opcode := r.buf.uint8()

		switch opcode {
		case lneEndSequence:
			r.state.EndSequence = true

		case lneSetAddress:
			r.state.Address = r.buf.addr()

		case lneDefineFile:
			if done, err := r.readFileEntry(); err != nil {
				r.buf.err = err
				return nil
			} else if done {
				r.buf.err = DecodeError{"line", startOff, "malformed DW_LNE_define_file operation"}
				return nil
			}
			r.updateFileEntry()
		}

		r.buf.skip(int(startOff + length - r.buf.off))

		if opcode == lneEndSequence {
			goto emit
		}

	// Standard opcodes [DWARF2 6.2.5.2]
	case lnsCopy:
		goto emit

	case lnsAdvancePC:
		r.state.Address += uint64(r.minInstructionLength) * r.buf.uint()

	case lnsAdvanceLine:
		r.state.Line += int(r.buf.int())

	case lnsSetFile:
		r.state.FileIndex = int(r.buf.uint())
		r.updateFileEntry()

	case lnsSetColumn:
		r.state.Column = int(r.buf.uint())

	case lnsNegateStmt:
		r.state.IsStmt = !r.state.IsStmt

	case lnsSetBasicBlock:
		r.state.BasicBlock = true

	case lnsConstAddPC:
		r.state.Address += uint64(r.minInstructionLength * ((255 - r.opcodeBase) / r.lineRange))

	case lnsFixedAdvancePC:
		r.state.Address += uint64(r.buf.uint16())

	default:
		// Unhandled standard opcode.  Skip the number of
		// arguments that the prologue says this opcode has.
		for i := 0; i < r.opcodeLengths[opcode]; i++ {
			r.buf.uint()
		}
	}
	return nil

emit:
	emitx := r.state
	r.state.BasicBlock = false
	return &emitx
}
