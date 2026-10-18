// Package darkknightcombo owns Dark Knight combo pages independently from
// ordinary learned skills and quick-slot layouts.
package darkknightcombo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

const (
	Job               byte = 9
	MaximumPageCount       = 2
	MaximumStageCount      = 5
	maximumEntryCount      = 255
)

var ErrPageBlockInvalid = errors.New("dark knight combo page block is invalid")

type Root struct {
	SkillID  uint16
	Children []uint16
}

type Page struct {
	Index byte
	Roots []Root
}

// PreloadEntry is one character's combo pages for the native character-select
// preloader. Slot is the client roster slot, not a database row ordinal.
type PreloadEntry struct {
	Slot  byte
	Pages []Page
}

// ParsePageBlock decodes the native editor page payload:
// u8 page, u8 root_count, repeated(u16 root, u8 child_count, u16 children...).
func ParsePageBlock(body []byte) (Page, error) {
	if len(body) < 2 {
		return Page{}, fmt.Errorf("%w: length=%d", ErrPageBlockInvalid, len(body))
	}
	page := Page{Index: body[0]}
	if page.Index >= MaximumPageCount {
		return Page{}, fmt.Errorf("%w: page=%d", ErrPageBlockInvalid, page.Index)
	}
	offset := 2
	rootCount := int(body[1])
	page.Roots = make([]Root, 0, rootCount)
	for rootIndex := 0; rootIndex < rootCount; rootIndex++ {
		if offset+3 > len(body) {
			return Page{}, fmt.Errorf("%w: truncated root=%d offset=%d", ErrPageBlockInvalid, rootIndex, offset)
		}
		root := Root{SkillID: binary.LittleEndian.Uint16(body[offset : offset+2])}
		offset += 2
		childCount := int(body[offset])
		offset++
		if offset+childCount*2 > len(body) {
			return Page{}, fmt.Errorf("%w: truncated children root=%d count=%d", ErrPageBlockInvalid, rootIndex, childCount)
		}
		root.Children = make([]uint16, 0, childCount)
		for childIndex := 0; childIndex < childCount; childIndex++ {
			root.Children = append(root.Children, binary.LittleEndian.Uint16(body[offset:offset+2]))
			offset += 2
		}
		page.Roots = append(page.Roots, root)
	}
	if offset != len(body) {
		return Page{}, fmt.Errorf("%w: trailing=%d", ErrPageBlockInvalid, len(body)-offset)
	}
	return page, nil
}

// NormalizePage keeps the last occurrence of a child skill, matching the
// native editor when a skill is moved within or between combo columns.
func NormalizePage(page Page) (Page, error) {
	if page.Index >= MaximumPageCount || len(page.Roots) > maximumEntryCount {
		return Page{}, ErrPageBlockInvalid
	}
	type occurrence struct{ root, child int }
	last := make(map[uint16]occurrence)
	for rootIndex, root := range page.Roots {
		if len(root.Children) > MaximumStageCount {
			return Page{}, ErrPageBlockInvalid
		}
		for childIndex, skillID := range root.Children {
			last[skillID] = occurrence{root: rootIndex, child: childIndex}
		}
	}
	out := Page{Index: page.Index, Roots: make([]Root, 0, len(page.Roots))}
	for rootIndex, root := range page.Roots {
		copyRoot := Root{SkillID: root.SkillID}
		for childIndex, skillID := range root.Children {
			if last[skillID] == (occurrence{root: rootIndex, child: childIndex}) {
				copyRoot.Children = append(copyRoot.Children, skillID)
			}
		}
		out.Roots = append(out.Roots, copyRoot)
	}
	return out, nil
}

func EncodePageBlock(page Page) ([]byte, error) {
	page, err := NormalizePage(page)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, 2+len(page.Roots)*3)
	body = append(body, page.Index, byte(len(page.Roots)))
	var scratch [2]byte
	for _, root := range page.Roots {
		binary.LittleEndian.PutUint16(scratch[:], root.SkillID)
		body = append(body, scratch[:]...)
		body = append(body, byte(len(root.Children)))
		for _, child := range root.Children {
			binary.LittleEndian.PutUint16(scratch[:], child)
			body = append(body, scratch[:]...)
		}
	}
	return body, nil
}

// EncodeNotificationBody builds the current client's class-0/op447 combo
// initialization body. applyAndEcho is deliberately zero: the server is
// restoring durable state and must not make the client immediately echo an
// op508 save while the character is still entering the scene.
func EncodeNotificationBody(pages []Page) ([]byte, error) {
	if len(pages) == 0 || len(pages) > MaximumPageCount {
		return nil, ErrPageBlockInvalid
	}
	ordered := append([]Page(nil), pages...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Index < ordered[right].Index })
	body := []byte{0, byte(len(ordered))}
	seen := make(map[byte]struct{}, len(ordered))
	for _, page := range ordered {
		if _, duplicate := seen[page.Index]; duplicate {
			return nil, ErrPageBlockInvalid
		}
		seen[page.Index] = struct{}{}
		block, err := EncodePageBlock(page)
		if err != nil {
			return nil, err
		}
		body = append(body, block...)
	}
	return body, nil
}

// EncodePreloadNotificationBody builds the current client's class-0/op809
// character-select preloader. Each entry owns its own page-count and the same
// page blocks accepted by class-0/op447. The list is omitted by the bridge
// when no Dark Knight roster row can be populated.
func EncodePreloadNotificationBody(entries []PreloadEntry) ([]byte, error) {
	if len(entries) == 0 || len(entries) > maximumEntryCount {
		return nil, ErrPageBlockInvalid
	}
	body := make([]byte, 0, 2)
	body = append(body, byte(len(entries)))
	seenSlots := make(map[byte]struct{}, len(entries))
	for _, entry := range entries {
		if _, duplicate := seenSlots[entry.Slot]; duplicate {
			return nil, ErrPageBlockInvalid
		}
		seenSlots[entry.Slot] = struct{}{}
		if len(entry.Pages) == 0 || len(entry.Pages) > MaximumPageCount {
			return nil, ErrPageBlockInvalid
		}
		ordered := append([]Page(nil), entry.Pages...)
		sort.Slice(ordered, func(left, right int) bool { return ordered[left].Index < ordered[right].Index })
		body = append(body, entry.Slot, 0, byte(len(ordered)))
		seenPages := make(map[byte]struct{}, len(ordered))
		for _, page := range ordered {
			if _, duplicate := seenPages[page.Index]; duplicate {
				return nil, ErrPageBlockInvalid
			}
			seenPages[page.Index] = struct{}{}
			block, err := EncodePageBlock(page)
			if err != nil {
				return nil, err
			}
			body = append(body, block...)
		}
	}
	return body, nil
}

func RootSkillIDs(pages []Page) map[uint16]struct{} {
	result := make(map[uint16]struct{})
	for _, page := range pages {
		for _, root := range page.Roots {
			result[root.SkillID] = struct{}{}
		}
	}
	return result
}

func ChildSkillIDs(page Page) map[uint16]struct{} {
	result := make(map[uint16]struct{})
	for _, root := range page.Roots {
		for _, child := range root.Children {
			result[child] = struct{}{}
		}
	}
	return result
}
