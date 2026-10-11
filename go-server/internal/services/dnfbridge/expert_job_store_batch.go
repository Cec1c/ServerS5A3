package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// Companion transports an explicit, finite selection from native 2C38840.
// Original five-byte requests remain unchanged. No time-based coalescing.
func parseCurrentExpertJobStoreDisjointBatch(body []byte) ([]currentExpertJobStoreDisjointRequest, error) {
	if len(body) == 5 {
		request, err := parseCurrentExpertJobStoreDisjointRequest(body)
		if err != nil {
			return nil, err
		}
		return []currentExpertJobStoreDisjointRequest{request}, nil
	}
	if len(body) < 12 || binary.LittleEndian.Uint16(body[2:4]) != math.MaxUint16 || !bytes.Equal(body[5:9], []byte{'B', '2', '3', 1}) {
		return nil, fmt.Errorf("invalid player disjoint batch header")
	}
	count := int(body[9])
	if count == 0 || count > currentDisjointBatchMaxItems || len(body) != 10+2*count {
		return nil, fmt.Errorf("invalid player disjoint batch count")
	}
	requests := make([]currentExpertJobStoreDisjointRequest, 0, count)
	seen := make(map[int16]bool, count)
	for i := 0; i < count; i++ {
		single := append([]byte(nil), body[:5]...)
		copy(single[2:4], body[10+2*i:12+2*i])
		request, err := parseCurrentExpertJobStoreDisjointRequest(single)
		if err != nil || seen[request.TargetSlot] {
			return nil, fmt.Errorf("invalid or duplicate player disjoint batch slot")
		}
		seen[request.TargetSlot] = true
		requests = append(requests, request)
	}
	return requests, nil
}

func mergeCurrentExpertJobBatchMaterial(materials *[]currentExpertJobExtractionMaterial, material currentExpertJobExtractionMaterial) error {
	for i := range *materials {
		entry := &(*materials)[i]
		if entry.Slot == material.Slot && entry.ItemID == material.ItemID {
			if uint64(entry.Count)+uint64(material.Count) > math.MaxUint32 {
				return fmt.Errorf("disjoint batch material overflow")
			}
			entry.Count += material.Count
			return nil
		}
	}
	if len(*materials) >= math.MaxUint8 {
		return fmt.Errorf("disjoint batch material rows overflow")
	}
	*materials = append(*materials, material)
	return nil
}
