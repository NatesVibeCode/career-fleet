// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package atom provides integer codes (also known as atoms) for a fixed set of
// frequently occurring HTML strings: tag names and attribute keys such as "p"
// and "id".
//
// Sharing an atom's name between all elements with the same tag can result in
// fewer string allocations when tokenizing and parsing HTML. Integer
// comparisons are also generally faster than string comparisons.
//
// The value of an atom's particular code is not guaranteed to stay the same
// between versions of this package. Neither is any ordering guaranteed:
// whether atom.H1 < atom.H2 may also change. The codes are not guaranteed to
// be dense. The only guarantees are that e.g. looking up "div" will yield
// atom.Div, calling atom.Div.String will return "div", and atom.Div != 0.
package parse // import "golang.org/x/net/html/atom"

// Atom is an integer code for a string. The zero value maps to "".
type fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Atom uint32

// String returns the atom's name.
func (a fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Atom) String() string {
	start := uint32(a >> 8)
	n := uint32(a & 0xff)
	if start+n > uint32(len(fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_atomText)) {
		return ""
	}
	return fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_atomText[start : start+n]
}

func (a fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Atom) string() string {
	return fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_atomText[a>>8 : a>>8+a&0xff]
}

// fnv computes the FNV hash with an arbitrary starting value h.
func fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_fnv(h uint32, s []byte) uint32 {
	for i := range s {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_match(s string, t []byte) bool {
	for i, c := range t {
		if s[i] != c {
			return false
		}
	}
	return true
}

// Lookup returns the atom whose name is s. It returns zero if there is no
// such atom. The lookup is case sensitive.
func fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Lookup(s []byte) fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Atom {
	if len(s) == 0 || len(s) > fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_maxAtomLen {
		return 0
	}
	h := fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_fnv(fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_hash0, s)
	if a := fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_table()[h&uint32(len(fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_table())-1)]; int(a&0xff) == len(s) && fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_match(a.string(), s) {
		return a
	}
	if a := fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_table()[(h>>16)&uint32(len(fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_table())-1)]; int(a&0xff) == len(s) && fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_match(a.string(), s) {
		return a
	}
	return 0
}

// String returns a string whose contents are equal to s. In that sense, it is
// equivalent to string(s) but may be more efficient.
func fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_String(s []byte) string {
	if a := fc_4670124f2944f5a7da274ba146884c248bda741a9d48f37fdf76f5a18b525ce0_Lookup(s); a != 0 {
		return a.String()
	}
	return string(s)
}
