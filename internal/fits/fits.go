// Package fits reads FITS image headers (and a sampled pixel summary) with no external
// dependencies. It implements just enough of the FITS standard for astrophotography:
// the primary HDU header (80-char ASCII cards in 2880-byte blocks) and a center-sampled
// statistic over the primary data array. Compressed (.fz) and gzipped FITS are not handled.
package fits

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	blockSize = 2880
	cardSize  = 80
)

// Card is one parsed header record.
type Card struct {
	Key     string
	Value   string // unquoted for string cards; raw token for numeric/logical cards
	Comment string
}

// Header is the ordered set of cards from one HDU, with case-insensitive lookup.
type Header struct {
	cards []Card
	index map[string]int
}

// File is an opened FITS file: its primary header plus the byte offset where data begins.
type File struct {
	Header     *Header
	DataOffset int64
	path       string
}

// Get returns the card for key (case-insensitive) and whether it was present.
func (h *Header) Get(key string) (Card, bool) {
	i, ok := h.index[strings.ToUpper(strings.TrimSpace(key))]
	if !ok {
		return Card{}, false
	}
	return h.cards[i], true
}

// String returns a string-valued card's value (trailing padding trimmed).
func (h *Header) String(key string) (string, bool) {
	c, ok := h.Get(key)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(c.Value), true
}

// Float parses a numeric card, tolerating Fortran 'D' exponents.
func (h *Header) Float(key string) (float64, bool) {
	c, ok := h.Get(key)
	if !ok {
		return 0, false
	}
	s := strings.TrimSpace(c.Value)
	s = strings.Map(func(r rune) rune {
		if r == 'D' || r == 'd' {
			return 'E'
		}
		return r
	}, s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Int parses an integer card, falling back to a float value truncated to int64.
func (h *Header) Int(key string) (int64, bool) {
	c, ok := h.Get(key)
	if !ok {
		return 0, false
	}
	s := strings.TrimSpace(c.Value)
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	if f, ok := h.Float(key); ok {
		return int64(f), true
	}
	return 0, false
}

// Bool parses a logical card (T/F).
func (h *Header) Bool(key string) (bool, bool) {
	c, ok := h.Get(key)
	if !ok {
		return false, false
	}
	return strings.TrimSpace(c.Value) == "T", true
}

// Open reads the primary HDU header of a FITS file.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, blockSize)
	hdr := &Header{index: make(map[string]int)}
	buf := make([]byte, blockSize)
	var consumed int64
	first := true

	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("read fits header block in %s: %w", path, err)
		}
		if first {
			key := strings.TrimRight(string(buf[0:8]), " ")
			if key != "SIMPLE" && key != "XTENSION" {
				return nil, fmt.Errorf("%s: not a FITS file (first keyword %q)", path, key)
			}
			first = false
		}
		consumed += blockSize
		done := false
		for off := 0; off < blockSize; off += cardSize {
			card, end := parseCard(buf[off : off+cardSize])
			if end {
				done = true
				break
			}
			if card.Key == "" {
				continue
			}
			if _, dup := hdr.index[card.Key]; !dup {
				hdr.index[card.Key] = len(hdr.cards)
			}
			hdr.cards = append(hdr.cards, card)
		}
		if done {
			break
		}
	}
	return &File{Header: hdr, DataOffset: consumed, path: path}, nil
}

// parseCard decodes one 80-byte card; the second return is true for the END card.
func parseCard(b []byte) (Card, bool) {
	key := strings.TrimRight(string(b[0:8]), " ")
	switch key {
	case "END":
		return Card{Key: "END"}, true
	case "":
		return Card{}, false
	case "COMMENT", "HISTORY":
		return Card{Key: key, Comment: strings.TrimRight(string(b[8:]), " ")}, false
	}
	rest := b[8:]
	if len(rest) >= 2 && rest[0] == '=' && rest[1] == ' ' {
		val, comment := parseValue(string(rest[2:]))
		return Card{Key: key, Value: val, Comment: comment}, false
	}
	return Card{Key: key, Comment: strings.TrimRight(string(rest), " ")}, false
}

// parseValue splits a card value field into value and comment, handling quoted strings.
func parseValue(s string) (value, comment string) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return "", ""
	}
	if s[0] == '\'' {
		var sb strings.Builder
		i := 1
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' { // escaped quote
					sb.WriteByte('\'')
					i += 2
					continue
				}
				i++ // closing quote
				break
			}
			sb.WriteByte(s[i])
			i++
		}
		value = strings.TrimRight(sb.String(), " ")
		if j := strings.Index(s[i:], "/"); j >= 0 {
			comment = strings.TrimSpace(s[i+j+1:])
		}
		return value, comment
	}
	if j := strings.Index(s, "/"); j >= 0 {
		return strings.TrimSpace(s[:j]), strings.TrimSpace(s[j+1:])
	}
	return strings.TrimSpace(s), ""
}
