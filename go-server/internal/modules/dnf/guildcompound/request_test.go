package guildcompound

import (
	"encoding/binary"
	"errors"
	"testing"
)

func compoundBody(count, grade uint32, refs [MaterialRefCount]uint32) []byte {
	body := make([]byte, RequestBodyLen)
	for index := 0; index < NativePrefixLen; index++ {
		body[index] = byte(0x80 + index)
	}
	binary.LittleEndian.PutUint32(body[countOffset:gradeOffset], count)
	binary.LittleEndian.PutUint32(body[gradeOffset:materialBase], grade)
	for index, ref := range refs {
		offset := materialBase + index*4
		binary.LittleEndian.PutUint32(body[offset:offset+4], ref)
	}
	return body
}

func TestDecodeRequestPreservesFields(t *testing.T) {
	refs := [MaterialRefCount]uint32{0x11223344, 0x55667788, 0x99AABBCC, 0xDDEEFF00}
	body := compoundBody(4, 3, refs)

	request, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if request.Count != 4 || request.Grade != 3 || request.MaterialRefs != refs {
		t.Fatalf("decoded request = %+v, want count=4 grade=3 refs=%v", request, refs)
	}
	for index := 0; index < NativePrefixLen; index++ {
		if request.Prefix[index] != byte(0x80+index) {
			t.Fatalf("prefix[%d] = 0x%02X, want 0x%02X", index, request.Prefix[index], byte(0x80+index))
		}
	}
}

func TestDecodeRequestTwoMaterials(t *testing.T) {
	refs := [MaterialRefCount]uint32{1001, 1002, AbsentMaterialRef, AbsentMaterialRef}
	request, err := DecodeRequest(compoundBody(2, 1, refs))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if request.Count != 2 || request.MaterialRefs != refs {
		t.Fatalf("decoded request = %+v, want count=2 refs=%v", request, refs)
	}
}

func TestDecodeRequestAcceptsStructuralCountRange(t *testing.T) {
	// 发送器 helper 的静态范围是 0..4。2..4 的业务下限尚未证实，
	// 因此 0 和 1 也必须通过结构解析，后续业务层才能据实拒绝。
	for count := uint32(0); count <= MaxMaterialCount; count++ {
		refs := [MaterialRefCount]uint32{AbsentMaterialRef, AbsentMaterialRef, AbsentMaterialRef, AbsentMaterialRef}
		for index := uint32(0); index < count; index++ {
			refs[index] = 2000 + index
		}
		request, err := DecodeRequest(compoundBody(count, 0, refs))
		if err != nil {
			t.Fatalf("count=%d: DecodeRequest: %v", count, err)
		}
		if request.Count != count {
			t.Fatalf("count=%d: decoded count=%d", count, request.Count)
		}
	}
}

func TestDecodeRequestReadsBusinessFieldsAfterOpaquePrefix(t *testing.T) {
	body := compoundBody(3, 6, [MaterialRefCount]uint32{11, 22, 33, AbsentMaterialRef})
	for index := 0; index < NativePrefixLen; index++ {
		body[index] = 0xFF
	}

	request, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if request.Count != 3 || request.Grade != 6 {
		t.Fatalf("decoded count=%d grade=%d, want 3 and 6", request.Count, request.Grade)
	}
}

func TestDecodeRequestRejectsWrongLength(t *testing.T) {
	for _, length := range []int{38, 50} {
		_, err := DecodeRequest(make([]byte, length))
		if !errors.Is(err, ErrRequestBodyLength) {
			t.Fatalf("length=%d: err=%v, want ErrRequestBodyLength", length, err)
		}
	}
}

func TestDecodeRequestRejectsEveryShortBodyWithoutPanic(t *testing.T) {
	for length := 0; length < RequestBodyLen; length++ {
		_, err := DecodeRequest(make([]byte, length))
		if !errors.Is(err, ErrRequestBodyLength) {
			t.Fatalf("length=%d: err=%v, want ErrRequestBodyLength", length, err)
		}
	}
}

func TestDecodeRequestRejectsMaterialCountAboveFour(t *testing.T) {
	for _, count := range []uint32{5, 0xFFFFFFFF} {
		_, err := DecodeRequest(compoundBody(count, 0, [MaterialRefCount]uint32{}))
		if !errors.Is(err, ErrMaterialCount) {
			t.Fatalf("count=%d: err=%v, want ErrMaterialCount", count, err)
		}
	}
}

func TestDecodeRequestRejectsNonAbsentUnusedMaterial(t *testing.T) {
	tests := []struct {
		name  string
		count uint32
		refs  [MaterialRefCount]uint32
	}{
		{
			name:  "first unused slot",
			count: 2,
			refs:  [MaterialRefCount]uint32{1, 2, 3, AbsentMaterialRef},
		},
		{
			name:  "last unused slot",
			count: 3,
			refs:  [MaterialRefCount]uint32{1, 2, 3, 4},
		},
		{
			name:  "no materials selected",
			count: 0,
			refs:  [MaterialRefCount]uint32{1, AbsentMaterialRef, AbsentMaterialRef, AbsentMaterialRef},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRequest(compoundBody(test.count, 0, test.refs))
			if !errors.Is(err, ErrUnusedMaterialRef) {
				t.Fatalf("err=%v, want ErrUnusedMaterialRef", err)
			}
		})
	}
}
