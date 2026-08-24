// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package jspacker

import (
	"fmt"
	"strings"
	"testing"
)

// pack builds a packed block the way the tool does: every keyword replaced by
// its index written in base radix.
func pack(t *testing.T, payload string, words []string, radix int) string {
	t.Helper()
	encoded := payload
	for i, w := range words {
		if w == "" {
			continue
		}
		encoded = strings.ReplaceAll(encoded, w, encodeBase(i, radix))
	}
	return fmt.Sprintf(`eval(function(p,a,c,k,e,d){}('%s',%d,%d,'%s'.split('|'),0,{}))`,
		strings.ReplaceAll(encoded, "'", `\'`), radix, len(words), strings.Join(words, "|"))
}

func encodeBase(n, radix int) string {
	if n == 0 {
		return string(alphabet[0])
	}
	var out []byte
	for n > 0 {
		out = append([]byte{alphabet[n%radix]}, out...)
		n /= radix
	}
	return string(out)
}

func TestUnpackRoundTrip(t *testing.T) {
	words := []string{"jwplayer", "sources", "file", "https://cdn.example/master.m3u8", "setup"}
	payload := `jwplayer("v").setup({sources:[{file:"https://cdn.example/master.m3u8"}]});`
	got, ok := Unpack(pack(t, payload, words, 36))
	if !ok {
		t.Fatal("Unpack refused a block it produced")
	}
	if got != payload {
		t.Fatalf("Unpack = %q, want %q", got, payload)
	}
}

func TestUnpackKeepsUnknownTokens(t *testing.T) {
	// Index 1 is empty, so its token must survive untouched, and any token
	// past the count too.
	src := `eval(function(p,a,c,k,e,d){}('0 1 z',36,2,'kept||'.split('|'),0,{}))`
	got, ok := Unpack(src)
	if !ok {
		t.Fatal("Unpack refused the block")
	}
	if got != "kept 1 z" {
		t.Fatalf("Unpack = %q", got)
	}
}

func TestUnpackHandlesEscapesAndQuotes(t *testing.T) {
	src := `eval(function(p,a,c,k,e,d){}('0=\'x\'',36,1,'a'.split('|'),0,{}))`
	got, ok := Unpack(src)
	if !ok || got != `a='x'` {
		t.Fatalf("Unpack = %q, %v", got, ok)
	}
	double := `eval(function(p,a,c,k,e,d){}("0",36,1,"a".split('|'),0,{}))`
	if got, ok := Unpack(double); !ok || got != "a" {
		t.Fatalf("double quotes: %q, %v", got, ok)
	}
}

func TestUnpackRefusesWhatItCannotRead(t *testing.T) {
	cases := map[string]string{
		"no block":        `<html>nothing</html>`,
		"no arguments":    `eval(function(p,a,c,k,e,d){})`,
		"payload missing": `eval(function(p,a,c,k,e,d){}(42,36,1,'a'.split('|'),0,{}))`,
		"radix missing":   `eval(function(p,a,c,k,e,d){}('0','x',1,'a'.split('|'),0,{}))`,
		"count missing":   `eval(function(p,a,c,k,e,d){}('0',36,'x','a'.split('|'),0,{}))`,
		"table missing":   `eval(function(p,a,c,k,e,d){}('0',36,1,42))`,
		"radix too small": `eval(function(p,a,c,k,e,d){}('0',1,1,'a'.split('|'),0,{}))`,
		"radix too big":   `eval(function(p,a,c,k,e,d){}('0',99,1,'a'.split('|'),0,{}))`,
		"unterminated":    `eval(function(p,a,c,k,e,d){}('0`,
	}
	for name, src := range cases {
		if got, ok := Unpack(src); ok {
			t.Errorf("%s: Unpack returned %q", name, got)
		}
	}
}

func TestUnpackAllReplacesEveryBlockAndKeepsTheRest(t *testing.T) {
	first := pack(t, `one("a")`, []string{"one"}, 36)
	second := pack(t, `two("b")`, []string{"two"}, 36)
	page := "<head>" + first + "</head><body>middle" + second + "end</body>"
	got := UnpackAll(page)
	for _, want := range []string{`one("a")`, `two("b")`, "<head>", "middle", "end</body>"} {
		if !strings.Contains(got, want) {
			t.Errorf("UnpackAll lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "eval(function") {
		t.Errorf("a block survived:\n%s", got)
	}
}

func TestUnpackAllSurvivesABlockItCannotRead(t *testing.T) {
	page := "before " + marker + " broken, after " + pack(t, `ok()`, []string{"ok"}, 36)
	got := UnpackAll(page)
	if !strings.Contains(got, "before") || !strings.Contains(got, "ok()") {
		t.Fatalf("UnpackAll = %q", got)
	}
	if !strings.Contains(got, marker) {
		t.Error("the unreadable block should be kept verbatim")
	}
}

func TestUnpackAllWithoutAnyBlock(t *testing.T) {
	if got := UnpackAll("<html>plain</html>"); got != "<html>plain</html>" {
		t.Fatalf("UnpackAll = %q", got)
	}
}

func TestDecode(t *testing.T) {
	if n, ok := decode("10", 36); !ok || n != 36 {
		t.Errorf("decode(10, 36) = %d, %v", n, ok)
	}
	if n, ok := decode("z", 36); !ok || n != 35 {
		t.Errorf("decode(z, 36) = %d, %v", n, ok)
	}
	if n, ok := decode("A", 62); !ok || n != 36 {
		t.Errorf("decode(A, 62) = %d, %v", n, ok)
	}
	if _, ok := decode("A", 36); ok {
		t.Error("a digit outside the radix was accepted")
	}
}

// TestJSIntRefusesANumberItCannotHold covers a run of digits too long to be a
// number: the unpacker must stop rather than take a wrapped one, which would
// decode the payload with the wrong base and yield plausible rubbish.
func TestJSIntRefusesANumberItCannotHold(t *testing.T) {
	long := strings.Repeat("9", 400)
	if _, _, ok := jsInt(long, 0); ok {
		t.Fatalf("a %d-digit number was accepted", len(long))
	}
	// The same shape within reach is read, so the refusal is about size.
	if n, next, ok := jsInt(" 62,", 0); !ok || n != 62 || next != 3 {
		t.Fatalf("jsInt(\" 62,\") = %d, %d, %v", n, next, ok)
	}
}
