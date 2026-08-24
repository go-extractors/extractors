// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package jspacker undoes the symbol substitution that video hosts apply to
// the script holding their stream URLs.
//
// The packed form is a self-extracting call: a payload where every identifier
// has been replaced by its index in a keyword table, written in base N. The
// page ships the table next to it, because the browser has to put it back
// together too. Unpacking is therefore a table lookup, not an attack on a
// protection measure.
package jspacker

import (
	"strconv"
	"strings"
)

// alphabet is the digit set the packer uses: 0-9, then a-z, then A-Z.
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// marker starts every packed block.
const marker = "eval(function(p,a,c,k,e,"

// Unpack expands the first packed block of src. It reports false when src
// holds none, or when the block is not the shape this packer produces.
func Unpack(src string) (string, bool) {
	expanded, _, ok := nextBlock(src)
	return expanded, ok
}

// UnpackAll replaces every packed block of src by what it expands to, leaving
// the rest of the page untouched, so a caller can scan the result as one text.
func UnpackAll(src string) string {
	var b strings.Builder
	for {
		i := strings.Index(src, marker)
		if i < 0 {
			b.WriteString(src)
			return b.String()
		}
		b.WriteString(src[:i])
		expanded, rest, ok := nextBlock(src[i:])
		if !ok {
			// Not a block this package understands: keep it verbatim and
			// move past the marker so the scan cannot loop.
			b.WriteString(src[i : i+len(marker)])
			src = src[i+len(marker):]
			continue
		}
		b.WriteString(expanded)
		src = rest
	}
}

// nextBlock expands the packed block src starts with, and returns what follows
// it.
func nextBlock(src string) (expanded, rest string, ok bool) {
	i := strings.Index(src, marker)
	if i < 0 {
		return "", "", false
	}
	// The arguments follow the function body, at its closing "}(" — but only
	// within this block: a truncated one must not reach into the next.
	limit := len(src)
	if next := strings.Index(src[i+len(marker):], marker); next >= 0 {
		limit = i + len(marker) + next
	}
	j := strings.Index(src[i:limit], "}(")
	if j < 0 {
		return "", "", false
	}
	p := i + j + len("}(")

	payload, p, okStr := jsString(src, p)
	if !okStr {
		return "", "", false
	}
	radix, p, okRadix := jsInt(src, p)
	if !okRadix {
		return "", "", false
	}
	count, p, okCount := jsInt(src, p)
	if !okCount {
		return "", "", false
	}
	table, p, okTable := jsString(src, p)
	if !okTable {
		return "", "", false
	}
	if radix < 2 || radix > len(alphabet) || count < 0 {
		return "", "", false
	}
	words := strings.Split(table, "|")
	return substitute(payload, radix, count, words), src[p:], true
}

// substitute replaces every base-N token of the payload by its keyword.
func substitute(payload string, radix, count int, words []string) string {
	var b strings.Builder
	b.Grow(len(payload) * 2)
	for i := 0; i < len(payload); {
		if !isWordByte(payload[i]) {
			b.WriteByte(payload[i])
			i++
			continue
		}
		j := i
		for j < len(payload) && isWordByte(payload[j]) {
			j++
		}
		token := payload[i:j]
		if n, ok := decode(token, radix); ok && n < count && n < len(words) && words[n] != "" {
			b.WriteString(words[n])
		} else {
			b.WriteString(token)
		}
		i = j
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}

// decode reads a token as a number in the packer's alphabet.
func decode(token string, radix int) (int, bool) {
	n := 0
	for i := 0; i < len(token); i++ {
		d := strings.IndexByte(alphabet[:radix], token[i])
		if d < 0 {
			return 0, false
		}
		n = n*radix + d
	}
	return n, true
}

// jsString reads the quoted string starting at or after i, honouring the
// backslash escapes a packer emits, and returns the offset just past it.
func jsString(s string, i int) (string, int, bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == ',' || s[i] == '\n' || s[i] == '\r' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || (s[i] != '\'' && s[i] != '"') {
		return "", i, false
	}
	quote := s[i]
	i++
	var b strings.Builder
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
		case c == quote:
			return b.String(), i + 1, true
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", i, false
}

// jsInt reads the integer argument starting at or after i.
func jsInt(s string, i int) (int, int, bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == ',' || s[i] == '\n' || s[i] == '\r' || s[i] == '\t') {
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i {
		return 0, i, false
	}
	n, err := strconv.Atoi(s[i:j])
	if err != nil {
		return 0, i, false
	}
	return n, j, true
}
