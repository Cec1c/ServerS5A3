package pvf

import (
	"bytes"
	"context"
	"fmt"
)

// VerifyRawReplacement proves preservation of every entry. Untouched groups
// must retain their encrypted bytes and metadata; touched groups are compared
// entry by entry after decompression. Shared pools and HASH must be identical.
func (a *Archive) VerifyRawReplacement(ctx context.Context, next *Archive, replacements []RawReplacement) (int, error) {
	return a.VerifyRawReplacementWithStrings(ctx, next, replacements, nil)
}

func (a *Archive) VerifyRawReplacementWithStrings(ctx context.Context, next *Archive, replacements []RawReplacement, added []string) (int, error) {
	if a == nil || next == nil || a.format != next.format || len(a.items) != len(next.items) || len(a.groups) != len(next.groups) {
		return 0, fmt.Errorf("archive format or counts changed")
	}
	x, err := a.layout()
	if err != nil {
		return 0, err
	}
	y, err := next.layout()
	if err != nil {
		return 0, err
	}
	wide, err := a.appendedWideStrings(added)
	if err != nil {
		return 0, err
	}
	nameSection := a.data[x.nameOffset : x.nameOffset+x.nameSize]
	if len(added) > 0 {
		nameSection, err = a.encodeProtectedNameSection(x, a.strA, wide)
		if err != nil {
			return 0, err
		}
	}
	if !bytes.Equal(a.strA, next.strA) || !bytes.Equal(wide, next.strW) ||
		!bytes.Equal(a.data[x.hashOffset:x.hashOffset+x.hashSize], next.data[y.hashOffset:y.hashOffset+y.hashSize]) ||
		!bytes.Equal(nameSection, next.data[y.nameOffset:y.nameOffset+y.nameSize]) {
		return 0, fmt.Errorf("shared strings, names or HASH changed")
	}
	oldHeader, newHeader := a.header.plain, next.header.plain
	// Only body compressed size may change in a raw replacement.
	copy(newHeader[32:36], oldHeader[32:36])
	if len(added) > 0 {
		copy(newHeader[44:48], oldHeader[44:48])
	}
	if oldHeader != newHeader {
		return 0, fmt.Errorf("unexpected header change")
	}
	allowed := map[int][]byte{}
	touched := map[int]bool{}
	for _, r := range replacements {
		i := a.FindFileIndex(r.Path)
		if i < 0 || next.FindFileIndex(r.Path) != i {
			return 0, fmt.Errorf("invalid replacement %q", r.Path)
		}
		if _, ok := allowed[i]; ok {
			return 0, fmt.Errorf("duplicate replacement %q", r.Path)
		}
		allowed[i] = r.Data
		touched[a.items[i].chunkIndex] = true
	}
	if len(allowed) == 0 {
		return 0, fmt.Errorf("empty replacements")
	}
	groups := map[int][]int{}
	for i, item := range a.items {
		if i%1024 == 0 && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		n := next.items[i]
		if item.nameOffset != n.nameOffset || item.pathOffset != n.pathOffset || item.dataType != n.dataType || item.chunkIndex != n.chunkIndex {
			return 0, fmt.Errorf("entry %d identity changed", i)
		}
		if touched[item.chunkIndex] {
			groups[item.chunkIndex] = append(groups[item.chunkIndex], i)
		} else if item != n {
			return 0, fmt.Errorf("unexpected metadata change at %d", i)
		}
	}
	for g := range a.groups {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if !touched[g] {
			b, e := a.rawChunk(x, g)
			if e != nil {
				return 0, e
			}
			c, e := next.rawChunk(y, g)
			if e != nil {
				return 0, e
			}
			if a.groups[g].originalSize != next.groups[g].originalSize || !bytes.Equal(b, c) {
				return 0, fmt.Errorf("untouched group %d changed", g)
			}
			continue
		}
		b, e := a.decodeChunkIndex(g)
		if e != nil {
			return 0, e
		}
		c, e := next.decodeChunkIndex(g)
		if e != nil {
			return 0, e
		}
		for _, i := range groups[g] {
			p, q := a.items[i], next.items[i]
			if p.dataOffset < 0 || p.dataSize < 0 || p.dataOffset > len(b) || p.dataSize > len(b)-p.dataOffset || q.dataOffset < 0 || q.dataSize < 0 || q.dataOffset > len(c) || q.dataSize > len(c)-q.dataOffset {
				return 0, fmt.Errorf("entry %d out of bounds", i)
			}
			want, ok := allowed[i]
			if !ok {
				want = b[p.dataOffset : p.dataOffset+p.dataSize]
			}
			if !bytes.Equal(want, c[q.dataOffset:q.dataOffset+q.dataSize]) {
				return 0, fmt.Errorf("entry %d raw content differs", i)
			}
		}
	}
	return len(a.items) - len(allowed), nil
}
