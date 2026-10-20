package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	"longheng.io/server/internal/modules/dnf/itemquality"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"strings"
)

var aikenRawAliases = []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"}
var aikenTailAliases = []string{"tail_data_72", "tail72", "mode1_raw_e_hex", "mode1_state_188_192_raw_hex"}

func aikenExtraBytes(value string) ([]byte, error) {
	return hex.DecodeString(strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", "0x", "", "0X", "").Replace(strings.TrimSpace(value)))
}

// Every present carrier must agree. Do not silently overwrite conflicting
// opaque instances, pad a short alias, or interpret state+C0 as raw+C0.
func currentAikenState(s repo.ItemStack) ([5]byte, error) {
	var state [5]byte
	found := false
	accept := func(b []byte) bool {
		if len(b) != 5 {
			return false
		}
		if found && !bytes.Equal(state[:], b) {
			return false
		}
		copy(state[:], b)
		found = true
		return true
	}
	row := func(b []byte) bool {
		return len(b) == 119 && (binary.LittleEndian.Uint32(b[2:6]) == 0 || binary.LittleEndian.Uint32(b[2:6]) == uint32(s.ItemID)) && accept(b[0x72:0x77])
	}
	if len(s.RawEntry) > 0 && !row(s.RawEntry) {
		return state, aiken.ErrInvalid
	}
	for _, k := range aikenRawAliases {
		if strings.TrimSpace(s.Extra[k]) != "" {
			b, e := aikenExtraBytes(s.Extra[k])
			if e != nil || !row(b) {
				return state, aiken.ErrInvalid
			}
		}
	}
	for _, k := range aikenTailAliases {
		if strings.TrimSpace(s.Extra[k]) != "" {
			b, e := aikenExtraBytes(s.Extra[k])
			if e != nil || !accept(b) {
				return state, aiken.ErrInvalid
			}
		}
	}
	if !found {
		return state, aiken.ErrInvalid
	}
	return state, nil
}
func currentAikenSetState(s repo.ItemStack, slot int16, state [5]byte) (repo.ItemStack, error) {
	old, err := currentAikenState(s)
	if err != nil || !bytes.Equal(old[:4], state[:4]) || old[4] >= 3 || state[4] != old[4]+1 {
		return repo.ItemStack{}, aiken.ErrInvalid
	}
	s = repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": s}}).Slots["x"]
	if s.Extra == nil {
		s.Extra = map[string]string{}
	}
	// The dedicated owner has already validated this template as PVF equipment.
	// Canonicalize only its kind so NOTI14 and cold projection preserve the
	// existing quality seed instead of interpreting raw+06 as stack quantity.
	s.Extra["item_kind"] = "equipment"
	if len(s.RawEntry) == 119 {
		s.RawEntry[0x76] = state[4]
	}
	for _, k := range aikenRawAliases {
		if strings.TrimSpace(s.Extra[k]) != "" {
			b, _ := aikenExtraBytes(s.Extra[k])
			b[0x76] = state[4]
			s.Extra[k] = hex.EncodeToString(b)
			if len(s.RawEntry) == 0 {
				s.RawEntry = append([]byte(nil), b...)
			}
		}
	}
	// Missing rows use the existing canonical projection; ItemID/slot/quality
	// come from repository metadata, not an invented template instance.
	if len(s.RawEntry) == 0 {
		entry := currentItemListEntryFromStack(0, slot, s)
		s.RawEntry = append([]byte(nil), entry.data[:]...)
		copy(s.RawEntry[0x72:0x77], state[:])
	}
	if len(s.RawEntry) != 119 {
		return repo.ItemStack{}, aiken.ErrInvalid
	}
	// A zero-ID opaque row is accepted by currentAikenState, but the generic
	// quality reader requires a matching row ID. Preserve its existing valid
	// seed as metadata without changing the row or overriding explicit quality.
	if binary.LittleEndian.Uint32(s.RawEntry[2:6]) == 0 && itemquality.FromStack(s) == 0 {
		if seed := binary.LittleEndian.Uint32(s.RawEntry[6:10]); itemquality.Valid(seed) {
			s.Extra["quality_seed"] = strconv.FormatUint(uint64(seed), 10)
		}
	}
	for _, k := range aikenTailAliases {
		if strings.TrimSpace(s.Extra[k]) != "" {
			s.Extra[k] = hex.EncodeToString(state[:])
		}
	}
	// Explicit mode1 state also makes public master login projection coherent.
	s.Extra["mode1_raw_e_hex"] = hex.EncodeToString(state[:])
	return s, nil
}

// Count owns stack quantity; synchronize the same quantity aliases as inventory
// owner, without treating count_or_instance (an instance identity) as quantity.
func currentAikenSetCount(s repo.ItemStack, count int64) (repo.ItemStack, error) {
	if count <= 0 || count > math.MaxUint32 {
		return repo.ItemStack{}, aiken.ErrInvalid
	}
	s = repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": s}}).Slots["x"]
	s.Count = count
	if len(s.RawEntry) == 119 {
		binary.LittleEndian.PutUint32(s.RawEntry[6:10], uint32(count))
	}
	for _, k := range aikenRawAliases {
		if strings.TrimSpace(s.Extra[k]) != "" {
			b, e := aikenExtraBytes(s.Extra[k])
			if e == nil && len(b) == 119 {
				binary.LittleEndian.PutUint32(b[6:10], uint32(count))
				s.Extra[k] = hex.EncodeToString(b)
			}
		}
	}
	for _, k := range []string{"amount_or_count", "amount", "count", "stack", "quantity"} {
		if _, ok := s.Extra[k]; ok {
			s.Extra[k] = strconv.FormatInt(count, 10)
		}
	}
	return s, nil
}
