package pvf

import (
	"bytes"
	"fmt"
)

// VerifyScriptReplacementPreservation proves unchanged files still reference
// identical encrypted chunks and string-pool offsets, without decompressing
// the entire archive into memory. Callers separately verify replacement text.
func (a *Archive) VerifyScriptReplacementPreservation(next *Archive, changedPaths []string) (int, error) {
	if a == nil || next == nil || a.format != FormatProtectedNKPI || next.format != a.format {
		return 0, fmt.Errorf("protected archives required")
	}
	if len(a.items) != len(next.items) || len(next.groups) != len(a.groups)+1 {
		return 0, fmt.Errorf("replacement changed file count or unexpected chunk count")
	}
	if !bytes.HasPrefix(next.strA, a.strA) || !bytes.HasPrefix(next.strW, a.strW) {
		return 0, fmt.Errorf("existing string-pool offsets changed")
	}
	oldLayout, err := a.layout()
	if err != nil {
		return 0, err
	}
	newLayout, err := next.layout()
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(a.data[oldLayout.bodyOffset:oldLayout.bodyOffset+oldLayout.bodySize], next.data[newLayout.bodyOffset:newLayout.bodyOffset+oldLayout.bodySize]) {
		return 0, fmt.Errorf("existing encrypted chunks changed")
	}
	for i, group := range a.groups {
		if group != next.groups[i] {
			return 0, fmt.Errorf("existing chunk %d changed", i)
		}
	}
	allowed := make(map[int]bool)
	for _, path := range changedPaths {
		i := a.FindFileIndex(path)
		if i < 0 || next.FindFileIndex(path) != i || allowed[i] {
			return 0, fmt.Errorf("invalid replacement path %q", path)
		}
		allowed[i] = true
	}
	unchanged := 0
	for i, item := range a.items {
		if allowed[i] {
			other := next.items[i]
			if item.nameOffset != other.nameOffset || item.pathOffset != other.pathOffset || item.dataType != other.dataType || other.chunkIndex != len(a.groups) {
				return 0, fmt.Errorf("replacement %d changed identity or type", i)
			}
			continue
		}
		if item != next.items[i] {
			return 0, fmt.Errorf("unexpected changed file %d", i)
		}
		unchanged++
	}
	return unchanged, nil
}
