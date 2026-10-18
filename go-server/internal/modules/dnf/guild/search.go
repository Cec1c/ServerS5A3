package guild

import (
	"errors"
	"math/bits"
	"strings"
	"unicode"
	"unicode/utf16"
)

type SearchMode byte

const (
	SearchGuildName SearchMode = iota
	SearchMasterName
	SearchTags
	SearchTextMaxUTF16 = 14
	SearchTextMaxBytes = 4 * SearchTextMaxUTF16
)

var ErrInvalidSearch = errors.New("guild search selection is invalid")

type SearchFilter struct {
	Mode    SearchMode
	Query   string
	TagMask uint32
}

// SearchDirectory preserves the existing level/ID ordering. Selected tags are
// conjunctive filters; zero means no tag filter. This is a local search policy,
// not evidence of the unavailable original server's ranking/matching policy.
func (r *Registry) SearchDirectory(filter SearchFilter, page, pageSize int) (int, []*Guild, error) {
	if page < 0 || pageSize <= 0 {
		return 0, nil, ErrInvalidSearch
	}
	switch filter.Mode {
	case SearchGuildName, SearchMasterName:
		if strings.IndexFunc(filter.Query, unicode.IsControl) >= 0 {
			return 0, nil, ErrInvalidSearch
		}
		if strings.TrimSpace(filter.Query) != "" {
			if err := validateText(filter.Query, SearchTextMaxBytes, false); err != nil {
				return 0, nil, ErrInvalidSearch
			}
		}
		if len(utf16.Encode([]rune(filter.Query))) > SearchTextMaxUTF16 {
			return 0, nil, ErrInvalidSearch
		}
	case SearchTags:
		if filter.TagMask & ^uint32(0x7f) != 0 || bits.OnesCount32(filter.TagMask) > 3 {
			return 0, nil, ErrInvalidSearch
		}
	default:
		return 0, nil, ErrInvalidSearch
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	all := r.Search("", 0, len(r.Guilds))
	matches := all[:0]
	for _, g := range all {
		var match bool
		switch filter.Mode {
		case SearchGuildName:
			match = strings.Contains(strings.ToLower(g.Name), query)
		case SearchMasterName:
			match = strings.Contains(strings.ToLower(g.MasterName), query)
		case SearchTags:
			mask := uint32(0)
			for _, tag := range g.Tags {
				if tag < 7 {
					mask |= 1 << tag
				}
			}
			match = mask&filter.TagMask == filter.TagMask
		}
		if match {
			matches = append(matches, g)
		}
	}
	total := len(matches)
	// Check before multiplying, including callers outside the byte-page protocol.
	if total == 0 || page > (total-1)/pageSize {
		return total, nil, nil
	}
	start := page * pageSize
	return total, matches[start : start+min(pageSize, total-start)], nil
}
