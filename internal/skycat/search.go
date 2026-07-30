package skycat

import (
	"sort"
	"strings"
)

// Ranking buckets for one query↔record comparison; lowest wins. Exactness dominates (a search for
// "M31" must not be buried under "M310"-style substring noise), then the field that matched: the
// primary designation outranks a common name, which outranks an alias — aliases are the noisiest
// field (every cross-catalogue number lands there).
const (
	rankExactName = iota
	rankExactCommon
	rankExactAlias
	rankPrefixName
	rankPrefixCommon
	rankPrefixAlias
	rankContainsCommon
	rankContainsName
	rankContainsAlias
	rankNone
)

// Search ranks catalogue records against a free-text query, so a UI can offer type-ahead over the
// whole merged catalogue (~15k rows) instead of only the objects that tonight's altitude-filtered
// ranked list happens to contain. Matching is case- and punctuation-insensitive (normalize) and
// covers the primary name, every alias and every OpenNGC common name — so "andromeda", "m 31" and
// "ngc224" all find the same record.
//
// Results are ordered exact → prefix → substring; ties break toward the better-known catalogue
// (Messier first) and then the brighter object. A blank query or limit <= 0 returns nil.
func (c *Catalog) Search(query string, limit int) []Record {
	q := normalize(query)
	if q == "" || limit <= 0 {
		return nil
	}
	type hit struct {
		rec  *Record
		rank int
	}
	hits := make([]hit, 0, 64)
	for _, r := range c.records {
		if rank := rankRecord(r, q); rank != rankNone {
			hits = append(hits, hit{rec: r, rank: rank})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		si, sj := sourceRank(hits[i].rec.Source), sourceRank(hits[j].rec.Source)
		if si != sj {
			return si < sj
		}
		return brighter(hits[i].rec, hits[j].rec)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Record, len(hits))
	for i, h := range hits {
		out[i] = *h.rec
	}
	return out
}

// rankRecord returns the best (lowest) bucket any of r's searchable fields achieves against the
// already-normalized query, or rankNone when nothing matches.
func rankRecord(r *Record, q string) int {
	best := rankToken(r.Name, q, rankExactName, rankPrefixName, rankContainsName)
	for _, cn := range r.CommonNames {
		if v := rankToken(cn, q, rankExactCommon, rankPrefixCommon, rankContainsCommon); v < best {
			best = v
		}
	}
	for _, a := range r.Aliases {
		if v := rankToken(a, q, rankExactAlias, rankPrefixAlias, rankContainsAlias); v < best {
			best = v
		}
	}
	return best
}

// rankToken scores one candidate string against the normalized query, mapping exact/prefix/substring
// hits onto the caller's bucket triple.
func rankToken(token, q string, exact, prefix, contains int) int {
	n := normalize(token)
	switch {
	case n == "":
		return rankNone
	case n == q:
		return exact
	case strings.HasPrefix(n, q):
		return prefix
	case strings.Contains(n, q):
		return contains
	}
	return rankNone
}

// sourceRank orders the catalogues by how likely a user means that designation: Messier objects are
// the famous ones, then NGC/IC, then the Sharpless/Lynds surveys.
func sourceRank(source string) int {
	switch source {
	case "messier":
		return 0
	case "ngc":
		return 1
	case "ic":
		return 2
	case "sh2":
		return 3
	case "ldn":
		return 4
	}
	return 5
}

// brighter reports whether a should sort before b on magnitude. A catalogued magnitude always beats
// an unknown one, so rows with real photometry surface first.
func brighter(a, b *Record) bool {
	switch {
	case a.HasMag && b.HasMag:
		return a.MagV < b.MagV
	case a.HasMag != b.HasMag:
		return a.HasMag
	}
	return false
}
