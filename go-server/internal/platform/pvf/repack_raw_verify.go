package pvf

import (
	"bytes"
	"fmt"
)

// VerifyRawReplacementPreservation checks an existing-file-only RepackRaw
// result. Unlike script additions, this must preserve the encrypted HASH and
// name tables exactly, as well as every unrelated file's original raw bytes.
// Only affected chunks are decompressed; unrelated chunks stay byte-identical.
func (a *Archive) VerifyRawReplacementPreservation(next *Archive, changedPaths []string) (int, error) {
	if a == nil || next == nil || a.format != next.format || len(changedPaths) == 0 ||
		len(a.items) != len(next.items) || len(a.groups) != len(next.groups) {
		return 0, fmt.Errorf("existing archives with equal file/chunk counts and explicit replacements required")
	}
	oldLayout, err := a.layout()
	if err != nil {
		return 0, err
	}
	newLayout, err := next.layout()
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(a.data[oldLayout.hashOffset:oldLayout.hashOffset+oldLayout.hashSize], next.data[newLayout.hashOffset:newLayout.hashOffset+newLayout.hashSize]) ||
		!bytes.Equal(a.data[oldLayout.nameOffset:oldLayout.nameOffset+oldLayout.nameSize], next.data[newLayout.nameOffset:newLayout.nameOffset+newLayout.nameSize]) {
		return 0, fmt.Errorf("existing HASH or string tables changed")
	}
	allowed, touched := map[int]bool{}, map[int]bool{}
	for _, path := range changedPaths {
		i := a.FindFileIndex(path)
		if i < 0 || next.FindFileIndex(path) != i || allowed[i] {
			return 0, fmt.Errorf("invalid replacement path %q", path)
		}
		allowed[i] = true
		touched[a.items[i].chunkIndex] = true
	}
	for i := range a.groups {
		if touched[i] {
			continue
		}
		x, err := a.rawChunk(oldLayout, i)
		if err != nil {
			return 0, err
		}
		y, err := next.rawChunk(newLayout, i)
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(x, y) || a.groups[i].originalSize != next.groups[i].originalSize {
			return 0, fmt.Errorf("unrelated chunk %d changed", i)
		}
	}
	unchanged := 0
	for i, item := range a.items {
		other := next.items[i]
		if item.nameOffset != other.nameOffset || item.pathOffset != other.pathOffset || item.dataType != other.dataType || item.chunkIndex != other.chunkIndex {
			return 0, fmt.Errorf("file %d identity/type/chunk changed", i)
		}
		if allowed[i] {
			continue
		}
		if touched[item.chunkIndex] {
			x, err := a.readRawIndex(i)
			if err != nil {
				return 0, err
			}
			y, err := next.readRawIndex(i)
			if err != nil {
				return 0, err
			}
			if !bytes.Equal(x, y) {
				return 0, fmt.Errorf("unrelated file %d changed", i)
			}
		} else if item != other {
			return 0, fmt.Errorf("unrelated file %d metadata changed", i)
		}
		unchanged++
	}
	return unchanged, nil
}
