// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"bytes"
	"fmt"
	"strconv"
)

type Toks struct {
	Toks []Tok
}

type Tok struct {
	Kind TokKind
	Text string
}

type TokKind uint8

const (
	TokKeyword TokKind = 1 + iota
	TokIdent
	TokNumber
	TokString
	TokChar
	TokOp
	TokEOF
)

func (t Tok) Int() int {
	if t.Kind != TokNumber {
		panic("token is not a number")
	}
	v, err := strconv.Atoi(t.Text)
	if err != nil {
		panic(err)
	}
	return v
}

func (t Tok) Match(kind TokKind, text string) bool {
	return t.Kind == kind && t.Text == text
}

var tokTab, puncTab, keyTab = mkTokTab([]string{
	"[", "]", "(", ")", "{", "}", ".", "->",
	"++", "--", "&", "*", "+", "-", "~", "!",
	"/", "%", "<<", ">>", "<", ">", "<=", ">=", "!=",
	"^", "|", "&&", "||",
	"?", ":", ";", "...",
	"=", "*=", "/=", "%=", "+=", "-=", "<<=", ">>=", "&=", "^=", "|=",
	",", "#", "##",
}, []string{"auto", "break", "case", "char", "const",
	"continue", "default", "do", "double", "else",
	"enum", "extern", "float", "for", "goto",
	"if", "inline", "int", "long", "register",
	"restrict", "return", "short", "signed", "sizeof",
	"static", "struct", "switch", "typedef", "union",
	"unsigned", "void", "volatile", "while", "_Bool",
	"_Complex", "_Imaginary",
})

var escTab = map[byte]byte{
	'\'': '\'', '"': '"', '?': '?', '\\': '\\',
	'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n',
	'r': '\r', 't': '\t', 'v': '\v'}

type chProps uint8

const (
	chNonDigit chProps = 1 << iota
	chDigit
	chOct
	chHex
	chChars // Character constant or string literal
	chPunct
)

func mkTokTab(punct, keywords []string) (tab [256]chProps, ptab map[string]bool, ktab map[string]bool) {
	for i := range tab {
		if i == '_' || 'a' <= i && i <= 'z' || 'A' <= i && i <= 'Z' {
			tab[i] |= chNonDigit
			if 'a' <= i && i <= 'f' || 'A' <= i && i <= 'F' {
				tab[i] |= chHex
			}
		} else if '0' <= i && i <= '9' {
			tab[i] |= chDigit | chHex
			if i <= '7' {
				tab[i] |= chOct
			}
		} else {
			switch i {
			case '\'', '"':
				tab[i] |= chChars
			}
		}
	}

	ptab = make(map[string]bool)
	for _, p := range punct {
		tab[p[0]] |= chPunct
		ptab[p] = true
	}

	ktab = make(map[string]bool)
	for _, k := range keywords {
		ktab[k] = true
	}

	return
}

type pos struct {
	path      string
	line, col int
}

func (p pos) error(f string, args ...interface{}) error {
	msg := fmt.Sprintf(f, args...)
	return fmt.Errorf("%s:%d:%d: %s", p.path, p.line, p.col, msg)
}

// charReader implements translation phases 1 and 2: mapping to the
// source character set, and deleting "\\\n". It doesn't implement
// trigraph sequences, so phase 1 is trivial.
type charReader struct {
	src []byte
	pos pos
}

func (r *charReader) ReadByte() byte {
	// Fold "\\\n".
	for len(r.src) >= 2 && r.src[0] == '\\' && r.src[1] == '\n' {
		r.pos.line++
		r.pos.col = 0
		r.src = r.src[2:]
	}
	if len(r.src) == 0 {
		return 0
	}
	next := r.src[0]
	r.src = r.src[1:]
	r.pos.col++
	if next == '\n' {
		r.pos.line++
		r.pos.col = 0
	}
	return next
}

func (r *charReader) EOF() bool {
	return len(r.src) == 0
}

// Tokenize parses src into C tokens. src must already be
// pre-processed.
func Tokenize(src []byte) ([]Tok, error) {
	toks := []Tok{}
	cr := charReader{src: src, pos: pos{"<none>", 1, 0}}

	var buf []byte
	var ch byte
	haveCh := false
	inLine, lineStart := false, 0 // Processing line directive

	for {
		if !haveCh {
			ch = cr.ReadByte()
		}
		haveCh = false

		if inLine && (ch == '\n' || cr.EOF()) {
			// Line directive complete.
			inLine = false
			lineToks := toks[lineStart:]
			toks = toks[:lineStart]
			cr.pos = pos{lineToks[2].Text, lineToks[1].Int(), 0}
		}

		if cr.EOF() {
			return toks, nil
		}

		// Skip whitespace.
		if bytes.IndexByte([]byte(" \t\n\v\f"), ch) >= 0 {
			continue
		}

		// Consume token.
		buf = append(buf[:0], ch)
		start := cr.pos
		switch {
		case tokTab[ch]&chNonDigit != 0:
			// Identifier or keyword
			for {
				ch = cr.ReadByte()
				if tokTab[ch]&(chNonDigit|chDigit) == 0 {
					haveCh = true
					break
				}
				buf = append(buf, ch)
			}
			if keyTab[string(buf)] {
				toks = append(toks, Tok{TokKeyword, string(buf)})
			} else {
				toks = append(toks, Tok{TokIdent, string(buf)})
			}

		case tokTab[ch]&chDigit != 0:
			// Number
			//
			// TODO: Floating-point constants.
			hex, oct := false, false
			for i := 0; ; i++ {
				ch = cr.ReadByte()
				if i == 0 && ch == 'x' {
					hex = true
				} else if i == 0 && ch == '0' {
					oct = true
				} else if hex && tokTab[ch]&chHex != 0 ||
					oct && tokTab[ch]&chOct != 0 ||
					tokTab[ch]&chDigit != 0 {
					// Consume
				} else {
					haveCh = true
					break
				}
				buf = append(buf, ch)
			}
			toks = append(toks, Tok{TokNumber, string(buf)})

		case tokTab[ch]&chChars != 0:
			// Character constant or string literal
			term := ch
			kind := "character constant"
			if ch == '"' {
				kind = "string literal"
			}
			buf = buf[:0]
			var escPos pos
			esc, hex, oct := false, 0, 0
			val := uint8(0)
			for {
				ch = cr.ReadByte()
			unread:
				if cr.EOF() {
					return nil, start.error("unterminated " + kind)
				}

				if esc {
					esc = false
					if rep, ok := escTab[ch]; ok {
						// Simple escape sequence
						buf = append(buf, rep)
					} else if ch == 'x' {
						// Hex escape sequence
						hex, val = 1, 0
					} else if tokTab[ch]&chOct != 0 {
						oct = 1
						val = ch - '0'
					} else {
						return nil, escPos.error("bad escape sequence")
					}
				} else if hex > 0 {
					if tokTab[ch]&chHex != 0 {
						hex++
						val = (val << 4) | hexVal(ch)
					} else if hex == 1 {
						return nil, escPos.error("bad escape sequence")
					} else {
						buf = append(buf, val)
						hex = 0
						goto unread
					}
				} else if oct > 0 {
					if oct <= 3 && tokTab[ch]&chOct != 0 {
						oct++
						val = (val << 3) | (ch - '0')
					} else {
						buf = append(buf, val)
						oct = 0
						goto unread
					}
				} else if ch == term {
					break
				} else if ch == '\\' {
					esc = true
					escPos = cr.pos
				} else if ch == '\n' {
					return nil, start.error("newline in " + kind)
				} else {
					buf = append(buf, ch)
				}
			}
			if term == '"' {
				toks = append(toks, Tok{TokString, string(buf)})
			} else {
				toks = append(toks, Tok{TokChar, string(buf)})
			}

		case tokTab[ch]&chPunct != 0:
			// Punctuation or line directive
			sol := cr.pos.col == 1
			for {
				ch = cr.ReadByte()
				buf = append(buf, ch)
				if !puncTab[string(buf)] {
					buf = buf[:len(buf)-1]
					haveCh = true
					break
				}
			}

			text := string(buf)
			if sol && text == "#" {
				// Line directive.
				lineStart, inLine = len(toks), true
			}
			toks = append(toks, Tok{TokOp, text})

		default:
			return nil, start.error("unexpected character %q", string(ch))
		}
	}
}

func hexVal(ch byte) uint8 {
	switch {
	case '0' <= ch && ch <= '9':
		return ch - '0'
	case 'a' <= ch && ch <= 'f':
		return ch - 'a' + 10
	case 'A' <= ch && ch <= 'F':
		return ch - 'A' + 10
	}
	panic("not a hex digit")
}

type toks []Tok

func (s toks) Next() Tok {
	if len(s) > 0 {
		return s[0]
	}
	return Tok{Kind: TokEOF, Text: "EOF"}
}

func (s toks) Peek(kind TokKind, text string) bool {
	return len(s) > 0 && s[0].Match(kind, text)
}

func (s *toks) Try(kind TokKind, text string) bool {
	if s.Peek(kind, text) {
		*s = (*s)[1:]
		return true
	}
	return false
}

func (s *toks) TryIdent() (Tok, bool) {
	tok := s.Next()
	if tok.Kind == TokIdent {
		s.Skip(1)
		return tok, true
	}
	return Tok{}, false
}

func (s *toks) Skip(n int) {
	if n > len(*s) {
		n = len(*s)
	}
	(*s) = (*s)[n:]
}

func (s *toks) SkipBalanced(until ...string) {
	level := 0
	for len(*s) != 0 {
		next := s.Next()
		if level == 0 {
			// Are we at a terminator?
			for _, u := range until {
				if next.Match(TokOp, u) {
					return
				}
			}
		}
		s.Skip(1)
		if next.Kind == TokOp {
			switch next.Text {
			case "{", "(", "[":
				level++
			case "]", ")", "}":
				level--
			}
		}
	}
}
