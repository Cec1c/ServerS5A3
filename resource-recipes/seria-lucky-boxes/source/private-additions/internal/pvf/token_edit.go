package pvf

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// EditToken specifies either unchanged payload bits or a new string value.
// Unlike TokenValue.Text (a read annotation), Text explicitly changes a reference.
type EditToken struct {
	Type    byte    `json:"type"`
	Payload *uint32 `json:"payload_u32,omitempty"`
	Text    *string `json:"text,omitempty"`
}
type TokenReplacement struct {
	Path   string
	Tokens []EditToken
}

// CompileTokenReplacements appends new UTF-16 strings, preserving every old
// offset. Numeric, float and unknown token payloads are copied without decoding.
func (a *Archive) CompileTokenReplacements(edits []TokenReplacement) ([]RawReplacement, []string, error) {
	if a == nil || a.format != FormatProtectedNKPI || len(a.strW)%2 != 0 {
		return nil, nil, fmt.Errorf("token edits require protected NKPI")
	}
	var result []RawReplacement
	var added []string
	magics := map[string]uint32{}
	offset := len(a.strW)
	for _, edit := range edits {
		entry, ok := a.FindFile(edit.Path)
		if !ok || entry.DataType != 1 || len(edit.Tokens) == 0 {
			return nil, nil, fmt.Errorf("token edit requires existing nonempty type-1 script: %s", edit.Path)
		}
		raw := make([]byte, len(edit.Tokens)*5)
		for i, t := range edit.Tokens {
			if (t.Payload == nil) == (t.Text == nil) {
				return nil, nil, fmt.Errorf("token %d requires exactly one payload or text", i)
			}
			var value uint32
			if t.Text != nil {
				if (t.Type != 3 && t.Type != 6) || strings.ContainsRune(*t.Text, 0) {
					return nil, nil, fmt.Errorf("invalid new string token %d", i)
				}
				var found bool
				value, found = magics[*t.Text]
				if !found {
					if offset > math.MaxInt32-1 {
						return nil, nil, fmt.Errorf("string pool too large")
					}
					value = uint32(offset + 1)
					magics[*t.Text] = value
					added = append(added, *t.Text)
					offset += len(utf16LEBytes(*t.Text)) + 2
				}
			} else {
				value = *t.Payload
			}
			raw[i*5] = t.Type
			binary.LittleEndian.PutUint32(raw[i*5+1:], value)
		}
		result = append(result, RawReplacement{Path: edit.Path, Data: raw})
	}
	return result, added, nil
}

func (a *Archive) appendedWideStrings(added []string) ([]byte, error) {
	if len(added) == 0 {
		return a.strW, nil
	}
	if a.format != FormatProtectedNKPI || len(a.strW)%2 != 0 {
		return nil, fmt.Errorf("string append requires protected NKPI")
	}
	wide := append([]byte(nil), a.strW...)
	for _, text := range added {
		if strings.ContainsRune(text, 0) {
			return nil, fmt.Errorf("string contains NUL")
		}
		wide = append(wide, utf16LEBytes(text)...)
		wide = append(wide, 0, 0)
	}
	return wide, nil
}
