// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import "encoding/binary"

type bufDecoder struct {
	buf   []byte
	order binary.ByteOrder
}

func (b *bufDecoder) skip(n int) {
	b.buf = b.buf[n:]
}

func (b *bufDecoder) bytes(x []byte) {
	copy(x, b.buf)
	b.buf = b.buf[len(x):]
}

func (b *bufDecoder) u16() uint16 {
	x := b.order.Uint16(b.buf)
	b.buf = b.buf[2:]
	return x
}

func (b *bufDecoder) u32() uint32 {
	x := b.order.Uint32(b.buf)
	b.buf = b.buf[4:]
	return x
}

func (b *bufDecoder) i32() int32 {
	x := int32(b.order.Uint32(b.buf))
	b.buf = b.buf[4:]
	return x
}

func (b *bufDecoder) u64() uint64 {
	x := b.order.Uint64(b.buf)
	b.buf = b.buf[8:]
	return x
}

func (b *bufDecoder) u64s(x []uint64) {
	for i := range x {
		x[i] = b.order.Uint64(b.buf[i*8:])
	}
	b.buf = b.buf[len(x)*8:]
}

func (b *bufDecoder) u32If(cond bool) uint32 {
	if cond {
		return b.u32()
	}
	return 0
}

func (b *bufDecoder) i32If(cond bool) int32 {
	if cond {
		return b.i32()
	}
	return 0
}

func (b *bufDecoder) u64If(cond bool) uint64 {
	if cond {
		return b.u64()
	}
	return 0
}

func (b *bufDecoder) cstring() string {
	for i, c := range b.buf {
		if c == 0 {
			x := string(b.buf[:i])
			b.buf = b.buf[i+1:]
			return x
		}
	}
	// TODO: Error?
	x := string(b.buf)
	b.buf = b.buf[:1]
	return x
}

func (b *bufDecoder) lenString() string {
	l := b.u32()
	if l > uint32(len(b.buf)) {
		// TODO: Error?
		l = uint32(len(b.buf))
	}
	str := (&bufDecoder{b.buf[:l], nil}).cstring()
	b.buf = b.buf[l:]
	return str
}

func (b *bufDecoder) stringList() []string {
	out := []string{}
	count := b.u32()
	for i := uint32(0); i < count; i++ {
		out = append(out, b.lenString())
	}
	return out
}
