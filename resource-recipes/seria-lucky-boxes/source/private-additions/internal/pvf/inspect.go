package pvf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// TokenDocument preserves payload bits and any non-token suffix. Text is only
// an annotation: reconstructing the bytes never depends on decoded strings.
type TokenDocument struct {
	Schema    int          `json:"schema"`
	Path      string       `json:"path"`
	RawSHA256 string       `json:"raw_sha256"`
	Tokens    []TokenValue `json:"tokens"`
	TailHex   string       `json:"tail_hex,omitempty"`
}

type TokenValue struct {
	Type    byte   `json:"type"`
	Payload uint32 `json:"payload_u32"`
	Text    string `json:"text,omitempty"`
}

func (a *Archive) InspectTokens(file File, raw []byte) (TokenDocument, error) {
	if file.DataType != 1 {
		return TokenDocument{}, fmt.Errorf("%s: tokens require data type 1, got %d", file.ArchivePath, file.DataType)
	}
	h := sha256.Sum256(raw)
	d := TokenDocument{Schema: 1, Path: file.ArchivePath, RawSHA256: hex.EncodeToString(h[:]), Tokens: make([]TokenValue, 0, len(raw)/5)}
	for pos := 0; pos+5 <= len(raw); pos += 5 {
		t := TokenValue{Type: raw[pos], Payload: binary.LittleEndian.Uint32(raw[pos+1 : pos+5])}
		if t.Type == 3 || t.Type == 6 {
			t.Text = a.ResolveString(int(int32(t.Payload)))
		}
		d.Tokens = append(d.Tokens, t)
	}
	d.TailHex = hex.EncodeToString(raw[len(raw)/5*5:])
	return d, nil
}

func (d TokenDocument) Raw() ([]byte, error) {
	if d.Schema != 1 {
		return nil, fmt.Errorf("unsupported token schema %d", d.Schema)
	}
	tail, err := hex.DecodeString(d.TailHex)
	if err != nil || len(tail) >= 5 {
		return nil, fmt.Errorf("invalid token tail")
	}
	out := make([]byte, len(d.Tokens)*5+len(tail))
	for i, t := range d.Tokens {
		out[i*5] = t.Type
		binary.LittleEndian.PutUint32(out[i*5+1:], t.Payload)
	}
	copy(out[len(d.Tokens)*5:], tail)
	return out, nil
}

// Text is a viewing representation, not a script compiler or lossless format.
func (a *Archive) Text(file File, raw []byte) (string, error) {
	switch file.DataType {
	case 1:
		if len(raw)%5 != 0 {
			return "", fmt.Errorf("%s: truncated script token; use raw or tokens", file.ArchivePath)
		}
		return a.decodeScript(raw), nil
	case 3:
		if len(raw)%2 != 0 {
			return "", fmt.Errorf("%s: odd UTF-16 byte count", file.ArchivePath)
		}
		return decodeUTF16LE(raw), nil
	default:
		return "", fmt.Errorf("%s: unsupported text data type %d; use raw", file.ArchivePath, file.DataType)
	}
}

// WalkRaw visits each selected entry once, in group/index order. It keeps one
// decompressed group at a time and never grows the archive's read caches.
// Raw slices are borrowed for the duration of the callback.
func (a *Archive) WalkRaw(ctx context.Context, prefix string, visit func(File, []byte) error) error {
	if a == nil || visit == nil {
		return fmt.Errorf("invalid archive or visitor")
	}
	groups := make(map[int][]int)
	key := pathKey(prefix)
	for idx, f := range a.files {
		if idx%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if archivePathWithinPrefix(f.ArchivePath, key) {
			groups[a.items[idx].chunkIndex] = append(groups[a.items[idx].chunkIndex], idx)
		}
	}
	order := make([]int, 0, len(groups))
	for g := range groups {
		order = append(order, g)
	}
	sort.Ints(order)
	for _, g := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := a.decodeChunkIndex(g)
		if err != nil {
			return err
		}
		for _, idx := range groups[g] {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := a.items[idx]
			if item.dataOffset < 0 || item.dataSize < 0 || item.dataOffset > len(chunk) || item.dataSize > len(chunk)-item.dataOffset {
				return fmt.Errorf("%w: file %d data range", ErrInvalidArchive, idx)
			}
			if err := visit(a.files[idx], chunk[item.dataOffset:item.dataOffset+item.dataSize]); err != nil {
				return err
			}
		}
	}
	return nil
}
