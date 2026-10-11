package pvf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

type ScriptStringChange struct{ Old, New string }
type ScriptToken struct {
	Type  byte
	Text  string
	Value int32
}
type ScriptAddition struct {
	Path, TemplatePath string
	Strings            []ScriptStringChange
	RemoveSections     []string
	AppendTokens       []ScriptToken
	AppendToExisting   bool
	// ReplaceTokens imports an authenticated token stream, including its
	// original float bit patterns and string-token kinds, without copying
	// unrelated sections from the type-1 layout template.
	ReplaceTokens bool
	// ReplaceExistingTokens rewrites one explicitly selected existing type-1
	// script. Callers supply the complete reviewed stream; untouched files and
	// existing string offsets are preserved by the same append-only repack.
	ReplaceExistingTokens bool
}

// RepackScriptAdditions clones proved type-1 templates into new paths. It
// appends strings (existing offsets remain stable), adds a single body chunk,
// and rebuilds HASH exactly as PvfHashTable.Build. Existing files stay intact.
func (a *Archive) RepackScriptAdditions(additions []ScriptAddition) ([]byte, error) {
	if a == nil || a.format != FormatProtectedNKPI || len(additions) == 0 {
		return nil, fmt.Errorf("script additions require protected NKPI and nonempty additions")
	}
	layout, err := a.layout()
	if err != nil {
		return nil, err
	}
	// Protected NKPI uses lowercase hash. Existing archives may retain hash
	// entries for deleted files; prove all live references before rebuilding
	// from the live table, as PvfHashTable.Build does.
	plain := append([]byte(nil), a.data[layout.hashOffset:layout.hashOffset+layout.hashSize]...)
	decryptProtected("hash", plain)
	if len(plain) < 8 {
		return nil, fmt.Errorf("truncated HASH")
	}
	n := int(binary.LittleEndian.Uint32(plain))
	if n > (len(plain)-8)/8 {
		return nil, fmt.Errorf("invalid HASH entry count")
	}
	end := 4 + 8*n
	lookup := int(binary.LittleEndian.Uint32(plain[end:]))
	if lookup > (len(plain)-end-4)/4 || end+4+4*lookup != len(plain) {
		return nil, fmt.Errorf("invalid HASH lookup count")
	}
	refs := make(map[[2]int]bool, n)
	for i := 0; i < n; i++ {
		pos := 4 + 8*i
		refs[[2]int{int(int32(binary.LittleEndian.Uint32(plain[pos:]))), int(int32(binary.LittleEndian.Uint32(plain[pos+4:])))}] = true
	}
	for _, item := range a.items {
		if !refs[[2]int{item.nameOffset, item.pathOffset}] {
			return nil, fmt.Errorf("HASH missing a live file reference")
		}
	}
	shadow := Archive{strA: append([]byte(nil), a.strA...), strW: append([]byte(nil), a.strW...)}
	// Index both existing pools once. Large authenticated imports repeat tags
	// and names; retaining their original magic prevents duplicate wide strings
	// and repeated scans of the complete ANSI pool.
	stringMagics := make(map[string]int)
	for start := 0; start < len(shadow.strA); {
		end := bytes.IndexByte(shadow.strA[start:], 0)
		if end < 0 {
			break
		}
		value := string(shadow.strA[start : start+end])
		if _, exists := stringMagics[value]; !exists {
			stringMagics[value] = start * 2
		}
		start += end + 1
	}
	for start := 0; start+1 < len(shadow.strW); {
		end := start
		for end+1 < len(shadow.strW) && (shadow.strW[end] != 0 || shadow.strW[end+1] != 0) {
			end += 2
		}
		if end+1 >= len(shadow.strW) {
			break
		}
		value := readUTF16String(shadow.strW, start)
		if _, exists := stringMagics[value]; !exists {
			stringMagics[value] = start + 1
		}
		start = end + 2
	}
	intern := func(text string) int {
		if magic, ok := stringMagics[text]; ok {
			return magic
		}
		// Keep newly added paths and script tags in the byte-string pool, as
		// the original file table and PvfLib's default string writer do.
		ascii := true
		for _, r := range text {
			if r > 127 {
				ascii = false
				break
			}
		}
		if ascii {
			magic := len(shadow.strA) * 2
			shadow.strA = append(shadow.strA, []byte(text)...)
			shadow.strA = append(shadow.strA, 0)
			stringMagics[text] = magic
			return magic
		}
		offset := len(shadow.strW) / 2
		shadow.strW = append(shadow.strW, utf16LEBytes(text)...)
		shadow.strW = append(shadow.strW, 0, 0)
		magic := offset*2 + 1
		stringMagics[text] = magic
		return magic
	}
	items := append([]fileItem(nil), a.items...)
	seen := make(map[string]bool)
	newChunk := []byte{}
	for _, addition := range additions {
		target := strings.ReplaceAll(strings.Trim(addition.Path, "/"), "\\", "/")
		existing := a.FindFileIndex(target)
		if target == "" || strings.Contains(target, "..") || (existing >= 0) != addition.AppendToExisting || seen[target] {
			return nil, fmt.Errorf("invalid or existing addition path %q", target)
		}
		seen[target] = true
		index := a.FindFileIndex(addition.TemplatePath)
		if addition.AppendToExisting {
			index = existing
		}
		if index < 0 || a.items[index].dataType != 1 {
			return nil, fmt.Errorf("addition template must be an existing type-1 script: %s", addition.TemplatePath)
		}
		raw, err := a.readRawIndex(index)
		if err != nil {
			return nil, err
		}
		raw = append([]byte(nil), raw...)
		if len(raw) == 0 || len(raw)%5 != 0 {
			return nil, fmt.Errorf("invalid template token stream")
		}
		if addition.ReplaceTokens || addition.ReplaceExistingTokens {
			if addition.ReplaceTokens == addition.ReplaceExistingTokens || addition.ReplaceExistingTokens != addition.AppendToExisting || len(addition.Strings) != 0 || len(addition.RemoveSections) != 0 || len(addition.AppendTokens) == 0 {
				return nil, fmt.Errorf("replacement tokens require one mode, matching existing-path ownership and a nonempty full stream")
			}
			raw = nil
		}
		for _, change := range addition.Strings {
			replacement := intern(change.New)
			matches := 0
			for pos := 0; pos+5 <= len(raw); pos += 5 {
				if (raw[pos] == 6 || raw[pos] == 3) && a.resolveString(int(int32(binary.LittleEndian.Uint32(raw[pos+1:])))) == change.Old {
					binary.LittleEndian.PutUint32(raw[pos+1:], uint32(replacement))
					matches++
				}
			}
			if matches == 0 {
				return nil, fmt.Errorf("template has no matching string token: %q", change.Old)
			}
		}
		for _, key := range addition.RemoveSections {
			out := make([]byte, 0, len(raw))
			removing, found := false, false
			for pos := 0; pos < len(raw); pos += 5 {
				if raw[pos] == 3 {
					removing = a.resolveString(int(int32(binary.LittleEndian.Uint32(raw[pos+1:])))) == key
					found = found || removing
				}
				if !removing {
					out = append(out, raw[pos:pos+5]...)
				}
			}
			if !found {
				return nil, fmt.Errorf("template section absent: %s", key)
			}
			raw = out
		}
		for _, token := range addition.AppendTokens {
			value := token.Value
			if token.Type == 3 || token.Type == 5 || token.Type == 6 || token.Type == 7 {
				value = int32(intern(token.Text))
			} else if token.Type != 0 && token.Type != 2 {
				return nil, fmt.Errorf("unsupported added token type %d", token.Type)
			}
			encoded := make([]byte, 5)
			encoded[0] = token.Type
			binary.LittleEndian.PutUint32(encoded[1:], uint32(value))
			raw = append(raw, encoded...)
		}
		split := strings.LastIndex(target, "/")
		dir, name := "", target
		if split >= 0 {
			// The native archive lookup compares the directory separately from
			// the basename. Match PvfArchive.SplitArchivePath: no trailing slash.
			dir, name = target[:split], target[split+1:]
		}
		item := fileItem{nameOffset: intern(name), pathOffset: intern(dir), chunkIndex: len(a.groups), dataOffset: len(newChunk), dataSize: len(raw), dataType: 1}
		if addition.AppendToExisting {
			item.nameOffset = items[existing].nameOffset
			item.pathOffset = items[existing].pathOffset
			items[existing] = item
		} else {
			items = append(items, item)
		}
		newChunk = append(newChunk, raw...)
	}
	nameSection, err := a.encodeProtectedNameSection(layout, shadow.strA, shadow.strW)
	if err != nil {
		return nil, err
	}
	unique := map[int]bool{}
	for _, item := range items {
		unique[item.nameOffset] = true
		if item.pathOffset >= 0 {
			unique[item.pathOffset] = true
		}
	}
	offsets := make([]int, 0, len(unique))
	for offset := range unique {
		offsets = append(offsets, offset)
	}
	sort.Slice(offsets, func(i, j int) bool {
		left, right := utf16.Encode([]rune(shadow.resolveString(offsets[i]))), utf16.Encode([]rune(shadow.resolveString(offsets[j])))
		for k := 0; k < len(left) && k < len(right); k++ {
			if left[k] != right[k] {
				return left[k] < right[k]
			}
		}
		return len(left) < len(right)
	})
	hash := make([]byte, 4+8*len(items)+4+4*len(offsets))
	binary.LittleEndian.PutUint32(hash, uint32(len(items)))
	for i, item := range items {
		binary.LittleEndian.PutUint32(hash[4+8*i:], uint32(item.nameOffset))
		binary.LittleEndian.PutUint32(hash[8+8*i:], uint32(item.pathOffset))
	}
	pos := 4 + 8*len(items)
	binary.LittleEndian.PutUint32(hash[pos:], uint32(len(offsets)))
	pos += 4
	for _, offset := range offsets {
		binary.LittleEndian.PutUint32(hash[pos:], uint32(offset))
		pos += 4
	}
	decryptProtected("hash", hash)
	encrypted, err := a.encryptChunk(newChunk)
	if err != nil {
		return nil, err
	}
	body := append([]byte(nil), a.data[layout.bodyOffset:layout.bodyOffset+layout.bodySize]...)
	body = append(body, encrypted...)
	groups := append([]groupItem(nil), a.groups...)
	groups = append(groups, groupItem{compressedSize: len(body), originalSize: len(newChunk)})
	header := a.header
	header.fileCount = len(items)
	header.groupCount = len(groups)
	header.bodySize = len(body)
	header.nameSize = len(nameSection)
	header.hashSize = len(hash)
	out := a.encodeHeader(header)
	out = append(out, encodeFileTable(items)...)
	out = append(out, hash...)
	out = append(out, nameSection...)
	out = append(out, a.encodeGroups(groups)...)
	out = append(out, body...)
	return out, nil
}
