package dnfbridge

import (
	"bytes"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestSceneTailKeepsExistingProfessionAcrossMode0Refresh(t *testing.T) {
	character := dnfrepo.CharacterRecord{Stats: map[string]int64{}}
	var empty packetWriter
	writeCurrentSceneObjectEntryTail(&empty, character, true)
	for _, kind := range []int64{1, 2, 3, 4} {
		character.Stats["expert_job_type"] = kind
		var projected packetWriter
		writeCurrentSceneObjectEntryTail(&projected, character, true)
		baseline, body := empty.bytes(), projected.bytes()
		if len(body) != len(baseline) {
			t.Fatal("profession changes tail shape")
		}
		differences := 0
		for index, value := range body {
			if value != baseline[index] {
				differences++
				if value != byte(kind) {
					t.Fatal("profession wire type differs from durable type")
				}
			}
		}
		if differences != 1 {
			t.Fatalf("type=%d modified %d fields", kind, differences)
		}
		var repeated packetWriter
		writeCurrentSceneObjectEntryTail(&repeated, character, true)
		if !bytes.Equal(body, repeated.bytes()) {
			t.Fatal("scene refresh lost profession")
		}
	}
	for _, kind := range []int64{-1, 0, 5} {
		character.Stats["expert_job_type"] = kind
		var invalid packetWriter
		writeCurrentSceneObjectEntryTail(&invalid, character, true)
		if !bytes.Equal(empty.bytes(), invalid.bytes()) {
			t.Fatal("invalid profession projected")
		}
	}
}

func TestSceneProfessionUsesNativeTypeExperiencePairAndNoDisguise(t *testing.T) {
	for _, kind := range []int64{1, 2, 3, 4} {
		for _, experience := range []int64{0, 10, 36, 811, 1195} {
			character := dnfrepo.CharacterRecord{Stats: map[string]int64{"expert_job_type": kind, "expert_job_exp": experience}}
			var w packetWriter
			writeCurrentSceneObjectEntryTail(&w, character, true)
			body := w.bytes()
			if body[59] != 0 || body[60] != 0 || body[61] != byte(kind) || binary.LittleEndian.Uint32(body[62:66]) != uint32(experience) {
				t.Fatalf("native profession pair/disguise = %x", body[59:66])
			}
		}
	}
}
