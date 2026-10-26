package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The set owner uses the same persisted Extra fields as the previous complete
// implementation. Record0 is duplicated in raw +0x75 because that byte is the
// key consumed by the client; the Extra field below is the durable carrier used
// to recover requiredParts, the ordered option list and the result grade.
const (
	aikenSetOptionsExtra       = "custom_ability_set_options"
	aikenSetRequiredPartsExtra = "custom_ability_set_required_parts"
	aikenSetGradeExtra         = "custom_ability_set_grade"
	aikenSetPartIndexExtra     = "custom_ability_set_part_index"
	aikenSetRecordExtra        = "custom_ability_set_record"

	aikenSetRawStart     = 0x72
	aikenSetRecordOffset = 0x75
	aikenSetRawEnd       = 0x77
)

var aikenSetExtraKeys = []string{
	aikenSetOptionsExtra,
	aikenSetRequiredPartsExtra,
	aikenSetGradeExtra,
	aikenSetPartIndexExtra,
	aikenSetRecordExtra,
}

// currentAikenSetCarrier reads the five-byte raw+0x72..0x76 carrier without
// requiring it to exist. A missing carrier is distinct from a broken one: the
// former lets ReadSet answer found=false for an ordinary item, while a supplied
// malformed or conflicting alias is rejected.
func currentAikenSetCarrier(s repo.ItemStack) ([5]byte, bool, error) {
	var state [5]byte
	found := false
	accept := func(b []byte) bool {
		if len(b) != len(state) {
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
		return len(b) == currentItemListEntryWireSize &&
			(binary.LittleEndian.Uint32(b[2:6]) == 0 || binary.LittleEndian.Uint32(b[2:6]) == uint32(s.ItemID)) &&
			accept(b[aikenSetRawStart:aikenSetRawEnd])
	}

	// A short opaque row is not a 0x77 tail carrier. Only the complete row has
	// the raw+0x72..0x76 meaning used here; other row layouts remain untouched.
	if len(s.RawEntry) == currentItemListEntryWireSize && !row(s.RawEntry) {
		return state, false, aiken.ErrInvalid
	}
	for _, key := range aikenRawAliases {
		if strings.TrimSpace(s.Extra[key]) != "" {
			b, err := aikenExtraBytes(s.Extra[key])
			if err != nil || !row(b) {
				return state, false, aiken.ErrInvalid
			}
		}
	}
	for _, key := range aikenTailAliases {
		if strings.TrimSpace(s.Extra[key]) != "" {
			b, err := aikenExtraBytes(s.Extra[key])
			if err != nil || !accept(b) {
				return state, false, aiken.ErrInvalid
			}
		}
	}
	return state, found, nil
}

func aikenSetExtraByte(s repo.ItemStack, key string) (byte, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(s.Extra[key]), 10, 8)
	if err != nil {
		return 0, aiken.ErrInvalid
	}
	return byte(value), nil
}

func currentAikenSetOptions(s repo.ItemStack) ([]byte, error) {
	text := strings.TrimSpace(s.Extra[aikenSetOptionsExtra])
	if text == "" {
		return nil, aiken.ErrInvalid
	}
	parts := strings.Split(text, ",")
	if len(parts) < 1 || len(parts) > 4 {
		return nil, aiken.ErrInvalid
	}
	options := make([]byte, 0, len(parts))
	seen := make(map[byte]struct{}, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 8)
		if err != nil || value == 0 {
			return nil, aiken.ErrInvalid
		}
		option := byte(value)
		if _, duplicate := seen[option]; duplicate {
			return nil, aiken.ErrInvalid
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	return options, nil
}

// currentAikenSetMetadata returns present=false only when no set fields remain.
// The historical cleared form custom_ability_set_record=0 is treated as empty;
// any of the other four fields makes the metadata explicit and subject to the
// complete consistency checks below.
func currentAikenSetMetadata(s repo.ItemStack) (options []byte, required, grade, partIndex, record byte, present bool, err error) {
	for _, key := range aikenSetExtraKeys[:4] {
		if strings.TrimSpace(s.Extra[key]) != "" {
			present = true
			break
		}
	}
	if value := strings.TrimSpace(s.Extra[aikenSetRecordExtra]); value != "" && value != "0" {
		present = true
	}
	if !present {
		return nil, 0, 0, 0, 0, false, nil
	}

	for _, key := range aikenSetExtraKeys {
		if strings.TrimSpace(s.Extra[key]) == "" {
			return nil, 0, 0, 0, 0, true, aiken.ErrInvalid
		}
	}
	options, err = currentAikenSetOptions(s)
	if err != nil {
		return nil, 0, 0, 0, 0, true, err
	}
	if required, err = aikenSetExtraByte(s, aikenSetRequiredPartsExtra); err != nil || required < 3 || required > 6 {
		return nil, 0, 0, 0, 0, true, aiken.ErrInvalid
	}
	if grade, err = aikenSetExtraByte(s, aikenSetGradeExtra); err != nil || grade > 3 {
		return nil, 0, 0, 0, 0, true, aiken.ErrInvalid
	}
	if partIndex, err = aikenSetExtraByte(s, aikenSetPartIndexExtra); err != nil || partIndex == 0 {
		return nil, 0, 0, 0, 0, true, aiken.ErrInvalid
	}
	if record, err = aikenSetExtraByte(s, aikenSetRecordExtra); err != nil || record == 0 {
		return nil, 0, 0, 0, 0, true, aiken.ErrInvalid
	}
	return options, required, grade, partIndex, record, true, nil
}

// currentAikenReadSet is intentionally limited to items that the caller has
// already validated as the Aiken template (item kind, PVF part-set membership
// and instance count). This helper owns persistence decoding only; repeating
// template/PVF validation here would require a catalog the record codec must
// not depend on. An ordinary item with no tail and no set metadata therefore
// returns found=false, while any explicit set metadata without a matching
// raw+0x75 carrier is corruption and returns an error.
//
// Selector is never persisted: the client resolves the current character and
// the caller supplies Selector for each 1278 notification.
func currentAikenReadSet(s repo.ItemStack) (aiken.Record, bool, error) {
	var record aiken.Record
	state, haveState, err := currentAikenSetCarrier(s)
	if err != nil {
		return record, false, err
	}
	options, required, grade, partIndex, storedRecord, present, err := currentAikenSetMetadata(s)
	if err != nil {
		return record, false, err
	}
	if !present {
		if haveState && state[3] != 0 {
			return record, false, aiken.ErrInvalid
		}
		return record, false, nil
	}
	if !haveState || state[3] == 0 {
		return record, false, aiken.ErrInvalid
	}
	if storedRecord != state[3] || partIndex != state[3] {
		return record, false, aiken.ErrInvalid
	}
	var slots [5]uint8
	copy(slots[:], options)
	return aiken.Record{
		Record0:  state[3],
		Record1:  required,
		Slots:    slots,
		Record18: grade,
	}, true, nil
}

func currentAikenValidateSetRecord(r aiken.Record) ([]byte, error) {
	if r.Record1 < 3 || r.Record1 > 6 || r.Record18 > 3 {
		return nil, aiken.ErrInvalid
	}
	options := r.Options()
	if len(options) < 1 || len(options) > 4 {
		return nil, aiken.ErrInvalid
	}
	seen := make(map[byte]struct{}, len(options))
	for _, option := range options {
		if option == 0 {
			return nil, aiken.ErrInvalid
		}
		if _, duplicate := seen[option]; duplicate {
			return nil, aiken.ErrInvalid
		}
		seen[option] = struct{}{}
	}
	return options, nil
}

// currentAikenWriteSet replaces only the set record byte and the five durable
// set fields. The other four bytes of the five-byte state (the member's own
// options/category/grade) are preserved through currentAikenWriteState, which
// also synchronizes RawEntry and every known raw/tail alias. Record0==0 is the
// clear form: all set fields are removed, raw+0x75 becomes zero, and unrelated
// Extra metadata survives. Selector is ignored and never stored.
func currentAikenWriteSet(s repo.ItemStack, slot int16, r aiken.Record) (repo.ItemStack, error) {
	state, _, err := currentAikenSetCarrier(s)
	if err != nil {
		return repo.ItemStack{}, err
	}
	var options []byte
	if r.Record0 == 0 {
		if r.Record1 != 0 || r.Record18 != 0 || r.Slots != ([5]uint8{}) {
			return repo.ItemStack{}, aiken.ErrInvalid
		}
	} else {
		options, err = currentAikenValidateSetRecord(r)
		if err != nil {
			return repo.ItemStack{}, err
		}
	}
	state[3] = r.Record0
	out, err := currentAikenWriteState(s, slot, state)
	if err != nil {
		return repo.ItemStack{}, err
	}
	if out.Extra == nil {
		out.Extra = map[string]string{}
	}
	if r.Record0 == 0 {
		for _, key := range aikenSetExtraKeys {
			delete(out.Extra, key)
		}
		return out, nil
	}
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, strconv.Itoa(int(option)))
	}
	out.Extra[aikenSetOptionsExtra] = strings.Join(values, ",")
	out.Extra[aikenSetRequiredPartsExtra] = strconv.Itoa(int(r.Record1))
	out.Extra[aikenSetGradeExtra] = strconv.Itoa(int(r.Record18))
	out.Extra[aikenSetPartIndexExtra] = strconv.Itoa(int(r.Record0))
	out.Extra[aikenSetRecordExtra] = strconv.Itoa(int(r.Record0))
	return out, nil
}
