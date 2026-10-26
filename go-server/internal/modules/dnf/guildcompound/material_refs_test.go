package guildcompound

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func materialRefsTestSlots(entries ...List38Slot) []List38Slot {
	return append([]List38Slot(nil), entries...)
}

func materialRefsTestRefs(values ...uint32) [MaterialRefCount]uint32 {
	refs := [MaterialRefCount]uint32{
		AbsentMaterialRef,
		AbsentMaterialRef,
		AbsentMaterialRef,
		AbsentMaterialRef,
	}
	for index, value := range values {
		refs[index] = value
	}
	return refs
}

func TestMaterialRefsPageBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		family MaterialFamily
		refs   [MaterialRefCount]uint32
		count  uint32
		slots  []List38Slot
		want   error
	}{
		{
			name:   "medal first and last slots",
			family: MaterialFamilyGuildMedal,
			refs:   materialRefsTestRefs(uint32(GuildMedalPageFirst), uint32(GuildMedalPageLast)),
			count:  2,
			slots: materialRefsTestSlots(
				List38Slot{Slot: GuildMedalPageFirst, ItemID: 91001, Quantity: 1},
				List38Slot{Slot: GuildMedalPageLast, ItemID: 91002, Quantity: 2},
			),
		},
		{
			name:   "guardian gem first and last slots",
			family: MaterialFamilyGuardianGem,
			refs:   materialRefsTestRefs(uint32(GuardianGemPageFirst), uint32(GuardianGemPageLast)),
			count:  2,
			slots: materialRefsTestSlots(
				List38Slot{Slot: GuardianGemPageFirst, ItemID: 90001, Quantity: 3},
				List38Slot{Slot: GuardianGemPageLast, ItemID: 90002, Quantity: 4},
			),
		},
		{
			name:   "medal rejects guardian page first slot",
			family: MaterialFamilyGuildMedal,
			refs:   materialRefsTestRefs(uint32(GuardianGemPageFirst)),
			count:  1,
			want:   ErrMaterialRefOutsidePage,
		},
		{
			name:   "guardian gem rejects medal page last slot",
			family: MaterialFamilyGuardianGem,
			refs:   materialRefsTestRefs(uint32(GuildMedalPageLast)),
			count:  1,
			want:   ErrMaterialRefOutsidePage,
		},
		{
			name:   "reference one past list38",
			family: MaterialFamilyGuildMedal,
			refs:   materialRefsTestRefs(List38SlotCount),
			count:  1,
			want:   ErrMaterialRefOutOfRange,
		},
		{
			name:   "absent sentinel inside selected count",
			family: MaterialFamilyGuardianGem,
			refs:   materialRefsTestRefs(AbsentMaterialRef),
			count:  1,
			want:   ErrMaterialRefAbsent,
		},
		{
			name:   "unspecified family",
			family: MaterialFamilyUnspecified,
			refs:   materialRefsTestRefs(uint32(GuildMedalPageFirst)),
			count:  1,
			want:   ErrMaterialRefOutsidePage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := ResolveMaterialRefs(test.family, test.count, test.refs, test.slots)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("err = %v, want %v", err, test.want)
				}
				if len(resolution.Materials) != 0 || resolution.TotalReferences != 0 || resolution.TotalRequestedUnits != 0 {
					t.Fatalf("failed resolution must be empty: %+v", resolution)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveMaterialRefs: %v", err)
			}
			if resolution.TotalReferences != int(test.count) || len(resolution.Materials) != int(test.count) {
				t.Fatalf("resolution = %+v, want %d materials", resolution, test.count)
			}
			if resolution.TotalRequestedUnits != test.count {
				t.Fatalf("TotalRequestedUnits = %d, want one unit per reference (%d)", resolution.TotalRequestedUnits, test.count)
			}
		})
	}
}

func TestMaterialRefsCountBounds(t *testing.T) {
	if _, err := ResolveMaterialRefs(MaterialFamilyGuildMedal, 0, materialRefsTestRefs(), nil); !errors.Is(err, ErrNoMaterialRefs) {
		t.Fatalf("count 0: err = %v, want ErrNoMaterialRefs", err)
	}
	if _, err := ResolveMaterialRefs(MaterialFamilyGuildMedal, MaxMaterialCount+1, materialRefsTestRefs(0, 1, 2, 3), nil); !errors.Is(err, ErrMaterialCount) {
		t.Fatalf("count above maximum: err = %v, want ErrMaterialCount", err)
	}
}

func TestMaterialRefsAggregatesRepeatedGuardianGemSlotInOrder(t *testing.T) {
	slots := materialRefsTestSlots(
		List38Slot{Slot: 57, ItemID: 90007, Quantity: 3},
		List38Slot{Slot: 58, ItemID: 90008, Quantity: 1},
	)
	resolution, err := ResolveMaterialRefs(
		MaterialFamilyGuardianGem,
		3,
		materialRefsTestRefs(57, 58, 57),
		slots,
	)
	if err != nil {
		t.Fatalf("ResolveMaterialRefs: %v", err)
	}
	want := []ResolvedMaterial{
		{Slot: 57, ItemID: 90007, Available: 3, Requested: 2},
		{Slot: 58, ItemID: 90008, Available: 1, Requested: 1},
	}
	if !reflect.DeepEqual(resolution.Materials, want) {
		t.Fatalf("materials = %+v, want %+v", resolution.Materials, want)
	}
	if resolution.TotalReferences != 3 || resolution.TotalRequestedUnits != 3 {
		t.Fatalf("resolution = %+v, want 3 references and 3 requested units", resolution)
	}
}

func TestMaterialRefsGuardianGemChecksRepeatedSlotQuantity(t *testing.T) {
	slots := materialRefsTestSlots(List38Slot{Slot: 57, ItemID: 90007, Quantity: 1})
	_, err := ResolveMaterialRefs(
		MaterialFamilyGuardianGem,
		2,
		materialRefsTestRefs(57, 57),
		slots,
	)
	if !errors.Is(err, ErrMaterialRefQuantity) {
		t.Fatalf("err = %v, want ErrMaterialRefQuantity", err)
	}
}

func TestMaterialRefsGuildMedalRepeatedSlotKeepsCountWithoutUnprovedQuantityRule(t *testing.T) {
	slots := materialRefsTestSlots(List38Slot{Slot: 7, ItemID: 90007, Quantity: 1})
	resolution, err := ResolveMaterialRefs(
		MaterialFamilyGuildMedal,
		2,
		materialRefsTestRefs(7, 7),
		slots,
	)
	if err != nil {
		t.Fatalf("ResolveMaterialRefs: %v", err)
	}
	want := []ResolvedMaterial{{Slot: 7, ItemID: 90007, Available: 1, Requested: 2}}
	if !reflect.DeepEqual(resolution.Materials, want) {
		t.Fatalf("materials = %+v, want %+v", resolution.Materials, want)
	}
	if resolution.TotalReferences != 2 || resolution.TotalRequestedUnits != 2 {
		t.Fatalf("resolution = %+v, want 2 references and 2 requested units", resolution)
	}
}

func TestMaterialRefsRejectsDuplicateSnapshotSlots(t *testing.T) {
	slots := materialRefsTestSlots(
		List38Slot{Slot: 7, ItemID: 90007, Quantity: 1},
		List38Slot{Slot: 7, ItemID: 90007, Quantity: 1},
	)
	_, err := ResolveMaterialRefs(MaterialFamilyGuildMedal, 1, materialRefsTestRefs(7), slots)
	if !errors.Is(err, ErrMaterialSnapshotDuplicate) {
		t.Fatalf("err = %v, want ErrMaterialSnapshotDuplicate", err)
	}
}

func TestMaterialRefsRejectsMissingSlot(t *testing.T) {
	slots := materialRefsTestSlots(List38Slot{Slot: 11, ItemID: 90011, Quantity: 1})
	_, err := ResolveMaterialRefs(MaterialFamilyGuildMedal, 1, materialRefsTestRefs(10), slots)
	if !errors.Is(err, ErrMaterialRefMissing) {
		t.Fatalf("err = %v, want ErrMaterialRefMissing", err)
	}
}

func TestMaterialRefsRejectsZeroQuantityAndEmptySlot(t *testing.T) {
	tests := []struct {
		name string
		slot List38Slot
		want error
	}{
		{
			name: "zero quantity",
			slot: List38Slot{Slot: 12, ItemID: 90012, Quantity: 0},
			want: ErrMaterialRefQuantity,
		},
		{
			name: "zero item id",
			slot: List38Slot{Slot: 12, ItemID: 0, Quantity: 5},
			want: ErrMaterialRefEmpty,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveMaterialRefs(
				MaterialFamilyGuildMedal,
				1,
				materialRefsTestRefs(uint32(test.slot.Slot)),
				materialRefsTestSlots(test.slot),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMaterialRefsOnlyCountedSlotsUseSentinelRule(t *testing.T) {
	slots := materialRefsTestSlots(
		List38Slot{Slot: GuardianGemPageFirst, ItemID: 90001, Quantity: 2},
		List38Slot{Slot: GuardianGemPageFirst + 1, ItemID: 90002, Quantity: 2},
	)
	refs := materialRefsTestRefs(uint32(GuardianGemPageFirst), uint32(GuardianGemPageFirst+1))
	resolution, err := ResolveMaterialRefs(MaterialFamilyGuardianGem, 2, refs, slots)
	if err != nil {
		t.Fatalf("trailing unused sentinels must be ignored: %v", err)
	}
	if len(resolution.Materials) != 2 {
		t.Fatalf("materials = %+v, want 2", resolution.Materials)
	}
	if resolution.Materials[0].Slot != GuardianGemPageFirst || resolution.Materials[1].Slot != GuardianGemPageFirst+1 {
		t.Fatalf("material order changed: %+v", resolution.Materials)
	}

	_, err = ResolveMaterialRefs(MaterialFamilyGuardianGem, 3, refs, slots)
	if !errors.Is(err, ErrMaterialRefAbsent) {
		t.Fatalf("sentinel inside count: err = %v, want ErrMaterialRefAbsent", err)
	}
}

func TestMaterialRefsDoesNotMutateInputs(t *testing.T) {
	slots := make([]List38Slot, 2, 4)
	slots[0] = List38Slot{Slot: 3, ItemID: 90003, Quantity: 2}
	slots[1] = List38Slot{Slot: 4, ItemID: 90004, Quantity: 1}
	fullSlots := slots[:cap(slots)]
	fullSlots[2] = List38Slot{Slot: 90, ItemID: 99990, Quantity: 9}
	fullSlots[3] = List38Slot{Slot: 91, ItemID: 99991, Quantity: 8}

	backingBefore := append([]List38Slot(nil), fullSlots...)
	refs := materialRefsTestRefs(3, 4)
	refsBefore := refs

	resolution, err := ResolveMaterialRefs(MaterialFamilyGuildMedal, 2, refs, slots)
	if err != nil {
		t.Fatalf("ResolveMaterialRefs: %v", err)
	}
	if len(resolution.Materials) != 2 {
		t.Fatalf("materials = %+v, want 2", resolution.Materials)
	}
	if refs != refsBefore {
		t.Fatalf("refs mutated: got %v, want %v", refs, refsBefore)
	}
	if !reflect.DeepEqual(fullSlots, backingBefore) {
		t.Fatalf("slots backing array mutated: got %v, want %v", fullSlots, backingBefore)
	}

	duplicateRefs := materialRefsTestRefs(3, 3)
	duplicateBefore := duplicateRefs
	if _, err = ResolveMaterialRefs(MaterialFamilyGuildMedal, 2, duplicateRefs, slots); err != nil {
		t.Fatalf("duplicate ResolveMaterialRefs: %v", err)
	}
	if duplicateRefs != duplicateBefore {
		t.Fatalf("duplicate call mutated refs: got %v, want %v", duplicateRefs, duplicateBefore)
	}
	if !reflect.DeepEqual(fullSlots, backingBefore) {
		t.Fatalf("duplicate call mutated slots backing array: got %v, want %v", fullSlots, backingBefore)
	}
}

func TestDecodeRequestUnusedSentinelIsRequiredOnlyAfterCount(t *testing.T) {
	body := make([]byte, RequestBodyLen)
	refs := materialRefsTestRefs(1, 2)
	binary.LittleEndian.PutUint32(body[countOffset:gradeOffset], 2)
	for index, ref := range refs {
		offset := materialBase + index*4
		binary.LittleEndian.PutUint32(body[offset:offset+4], ref)
	}
	request, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if request.Count != 2 || request.MaterialRefs != refs {
		t.Fatalf("request = %+v, want count 2 refs %v", request, refs)
	}

	for index := int(request.Count); index < MaterialRefCount; index++ {
		offset := materialBase + index*4
		binary.LittleEndian.PutUint32(body[offset:offset+4], 3)
		if _, err := DecodeRequest(body); !errors.Is(err, ErrUnusedMaterialRef) {
			t.Fatalf("unused index %d: err = %v, want ErrUnusedMaterialRef", index, err)
		}
		binary.LittleEndian.PutUint32(body[offset:offset+4], AbsentMaterialRef)
	}
}
