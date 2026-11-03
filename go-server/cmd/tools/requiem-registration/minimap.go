package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/town"
	archive "longheng.io/server/internal/platform/pvf"
	"strings"
)

type token struct {
	tag   string
	raw   []byte
	value int32
}

func tokens(raw []byte, resolve func(int) string) ([]token, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, fmt.Errorf("invalid raw token stream")
	}
	out := make([]token, 0, len(raw)/5)
	for i := 0; i < len(raw); i += 5 {
		v := int32(binary.LittleEndian.Uint32(raw[i+1:]))
		t := token{raw: raw[i : i+5], value: v}
		if raw[i] == 3 {
			t.tag = strings.ToLower(resolve(int(v)))
		}
		out = append(out, t)
	}
	return out, nil
}
func townIDs(ts []token) ([]int32, error) {
	var ids []int32
	inTown := false
	for i, t := range ts {
		switch t.tag {
		case "[town]":
			if inTown {
				return nil, fmt.Errorf("nested town")
			}
			inTown = true
			if i+2 >= len(ts) || ts[i+1].tag != "[index]" || ts[i+2].raw[0] != 0 {
				return nil, fmt.Errorf("town index is missing")
			}
			ids = append(ids, ts[i+2].value)
		case "[/town]":
			if !inTown {
				return nil, fmt.Errorf("orphan town close")
			}
			inTown = false
		}
	}
	if inTown {
		return nil, fmt.Errorf("unclosed town")
	}
	return ids, nil
}
func patch(raw []byte, resolve func(int) string) ([]byte, error) {
	ts, err := tokens(raw, resolve)
	if err != nil {
		return nil, err
	}
	ids, err := townIDs(ts)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id == 13 {
			return nil, fmt.Errorf("town 13 is already registered; review instead of applying twice")
		}
	}
	tags := map[string][]byte{}
	at := -1
	for i, t := range ts {
		if t.tag != "" {
			tags[t.tag] = t.raw
		}
		if at < 0 && t.tag == "[town]" {
			at = i * 5
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("no existing town anchor")
	}
	// The real rectangle encloses the existing target map's walkable rectangles.
	// The projected rectangle occupies unused space below the existing Time Gate
	// on the Shonan minimap. No tile/image/frame or map resource is substituted.
	var added []byte
	tag := func(s string) error {
		b, ok := tags[s]
		if !ok {
			return fmt.Errorf("required tag %s absent", s)
		}
		added = append(added, b...)
		return nil
	}
	integer := func(v int32) {
		b := make([]byte, 5)
		b[0] = 0
		binary.LittleEndian.PutUint32(b[1:], uint32(v))
		added = append(added, b...)
	}
	for _, part := range []struct {
		name   string
		values []int32
	}{
		{"[town]", nil}, {"[index]", []int32{13}}, {"[area]", nil}, {"[index]", []int32{0}},
		{"[type]", []int32{4}}, {"[real rect]", []int32{56, 180, 883, 354}},
		{"[project rect]", []int32{133, 270, 239, 309}}, {"[/area]", nil}, {"[/town]", nil},
	} {
		if err := tag(part.name); err != nil {
			return nil, err
		}
		for _, v := range part.values {
			integer(v)
		}
	}
	out := make([]byte, 0, len(raw)+len(added))
	out = append(out, raw[:at]...)
	out = append(out, added...)
	out = append(out, raw[at:]...)
	return out, nil
}
func repairMinimap(a *archive.Archive) ([]byte, int, error) {
	raw, err := a.ReadRaw(minimapPath)
	if err != nil {
		return nil, 0, err
	}
	patched, err := patch(raw, a.ResolveString)
	if err != nil {
		return nil, 0, err
	}
	// Verify the actual target still has the original quest/level and dungeon gate.
	towns, err := town.Load(context.Background(), a, town.Options{})
	if err != nil {
		return nil, 0, err
	}
	ar, ok := towns.FindArea(13, 0)
	if !ok || ar.MinLevel != 85 || len(ar.NeedQuests) != 1 || ar.NeedQuests[0] != 12855 || ar.DungeonGate == nil || *ar.DungeonGate != 18 || ar.MapPath != "Town/TimeGateRequiem/TimeGateRequiemWR.map" {
		return nil, 0, fmt.Errorf("reviewed town 13/0 contract changed")
	}
	data, err := a.RepackRaw([]archive.RawReplacement{{Path: minimapPath, Data: patched}})
	if err != nil {
		return nil, 0, err
	}
	next, err := archive.OpenBytes(data)
	if err != nil {
		return nil, 0, err
	}
	count, err := a.VerifyRawReplacementPreservation(next, []string{minimapPath})
	if err != nil {
		return nil, 0, err
	}
	actual, err := next.ReadRaw(minimapPath)
	if err != nil || !bytes.Equal(actual, patched) {
		return nil, 0, fmt.Errorf("raw readback mismatch: %v", err)
	}
	return data, count, nil
}
