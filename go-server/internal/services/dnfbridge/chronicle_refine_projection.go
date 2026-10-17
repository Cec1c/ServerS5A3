package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前119字节库存行的精炼块为+0x27..+0x37。
// 当前EXE 0x2437a60读取计数，0x2437a84读取两组ID/职业/转职/部位/选项。
const currentChronicleOptionsOffset = 0x27

func encodeCurrentChronicleOptions(options []dnfchronicle.Option) []byte {
	out := make([]byte, 17)
	out[0] = byte(len(options))
	for i, o := range options {
		binary.LittleEndian.PutUint32(out[1+i*4:], o.AuraID)
		out[9+i] = o.Job
		out[11+i] = o.Grow
		out[13+i] = o.EquipmentType
		out[15+i] = o.Number
	}
	return out
}
func decodeCurrentChronicleOptions(raw []byte) ([]dnfchronicle.Option, error) {
	if len(raw) != 17 || raw[0] > 2 {
		return nil, dnfchronicle.ErrTarget
	}
	out := make([]dnfchronicle.Option, int(raw[0]))
	for i := range out {
		out[i] = dnfchronicle.Option{AuraID: binary.LittleEndian.Uint32(raw[1+i*4:]), Job: raw[9+i], Grow: raw[11+i], EquipmentType: raw[13+i], Number: raw[15+i]}
	}
	return out, dnfchronicle.ValidateOptions(out)
}
func currentChronicleOptions(extra map[string]string, raw []byte) ([]dnfchronicle.Option, error) {
	if value, ok := extra["chronicle_options_v1"]; ok {
		data, err := hex.DecodeString(value)
		if err != nil {
			return nil, dnfchronicle.ErrTarget
		}
		return decodeCurrentChronicleOptions(data)
	}
	if len(raw) == currentItemListEntryWireSize {
		return decodeCurrentChronicleOptions(raw[currentChronicleOptionsOffset : currentChronicleOptionsOffset+17])
	}
	if data := currentItemListFixedExtraBytes(extra, 17, "middle_data_1a", "middle1a", "raw_data_1a"); len(data) == 17 {
		return decodeCurrentChronicleOptions(data)
	}
	return nil, nil
}
func currentSetChronicleOptions(stack dnfrepo.ItemStack, options []dnfchronicle.Option) (dnfrepo.ItemStack, error) {
	if err := dnfchronicle.ValidateOptions(options); err != nil {
		return dnfrepo.ItemStack{}, err
	}
	if stack.Extra == nil {
		stack.Extra = map[string]string{}
	}
	data := encodeCurrentChronicleOptions(options)
	encoded := hex.EncodeToString(data)
	stack.Extra["chronicle_options_v1"] = encoded
	stack.Extra["middle_data_1a"] = encoded
	stack.Extra["chronicle_option_count"] = strconv.Itoa(len(options))
	stack.Extra["mode1_sub_1d6e020_records_hex"] = hex.EncodeToString(currentChronicleVector(options))
	return stack, nil
}
func currentApplyChronicleOptionsToEntry(entry *currentItemListEntry, extra map[string]string) {
	if _, ok := extra["chronicle_options_v1"]; !ok {
		return
	}
	options, err := currentChronicleOptions(extra, nil)
	if err != nil {
		return
	}
	copy(entry.data[currentChronicleOptionsOffset:currentChronicleOptionsOffset+17], encodeCurrentChronicleOptions(options))
}
func currentChronicleVector(options []dnfchronicle.Option) []byte {
	out := make([]byte, len(options)*8)
	for i, o := range options {
		off := i * 8
		binary.LittleEndian.PutUint32(out[off:], o.AuraID)
		copy(out[off+4:], []byte{o.Job, o.Grow, o.EquipmentType, o.Number})
	}
	return out
}
func currentChronicleMode1Vector(extra map[string]string, entry currentItemListEntry) []byte {
	if _, own := extra["chronicle_options_v1"]; own {
		options, err := currentChronicleOptions(extra, nil)
		if err != nil {
			return nil
		}
		return currentChronicleVector(options)
	}
	if options, err := currentChronicleOptions(extra, entry.data[:]); err == nil && len(options) > 0 {
		return currentChronicleVector(options)
	}
	return currentMode1EquipmentVectorRecords(extra, "mode1_sub_1d6e020_records_hex", "mode1_vector_records_hex")
}
