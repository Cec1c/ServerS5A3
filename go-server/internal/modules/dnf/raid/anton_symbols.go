package raid

import (
	"fmt"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The symbol file stores integer-before-tag pairs. Section values are the next
// symbol's ID, so treating it as a usual [tag] value table shifts every ID.
func LoadAntonSymbols(source dnfpvf.Source) (map[string]uint32, error) {
	return loadRaidSymbols(source, "etc/raid/anton.etc.symbol")
}

func loadRaidSymbols(source dnfpvf.Source, path string) (map[string]uint32, error) {
	text, err := source.ReadText(path)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return nil, err
	}
	if len(doc.Tokens) == 0 || len(doc.Tokens)%2 != 0 {
		return nil, fmt.Errorf("incomplete Anton symbol table")
	}
	result := map[string]uint32{}
	ids := map[uint32]bool{}
	for i := 0; i < len(doc.Tokens); i += 2 {
		id, err := antonUint(doc.Tokens[i])
		name := strings.ToUpper(strings.TrimSpace(doc.Tokens[i+1].Value))
		if err != nil || id == 0 || ids[id] || doc.Tokens[i+1].Kind != dnfpvf.TokenSection || name == "" || result[name] != 0 {
			return nil, fmt.Errorf("invalid/repeated Anton symbol entry")
		}
		result[name] = id
		ids[id] = true
	}
	if len(result) > 255 {
		return nil, fmt.Errorf("Anton symbol table exceeds native count")
	}
	return result, nil
}

// Current NOTI583 -> 01FEAAC0 is u8 count followed by u32 ID/value pairs.
// The all-zero pair is a native reset sentinel and must be sent separately;
// the reader ends immediately after it. Ordinary projections reject that ID.
func BuildSymbolBody(symbols map[uint32]uint32) ([]byte, error) {
	if len(symbols) > 255 {
		return nil, fmt.Errorf("raid symbols exceed native count")
	}
	ids := make([]uint32, 0, len(symbols))
	for id := range symbols {
		if id == 0 {
			return nil, fmt.Errorf("raid symbol zero is reserved for reset")
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var writer packetWriter
	writer.writeByte(byte(len(ids)))
	for _, id := range ids {
		writer.writeUint32(id)
		writer.writeUint32(symbols[id])
	}
	return writer.bytes(), nil
}

func (s *AntonProgression) NativeSymbols() map[uint32]uint32 {
	if s == nil {
		return nil
	}
	result := make(map[uint32]uint32, len(s.program.symbols))
	for name, id := range s.program.symbols {
		result[id] = s.symbols[name]
	}
	s.appendVolcanoNativeSymbol(result)
	return result
}

// A reconnect restores current values without replaying past death, kick,
// phase-transition or one-shot AVI instructions. The derived Anton 111 latch
// is current state: restoring it lets the controller restore the barrier flag
// even when the recipient missed its edge. Anton IDs 126/127 enter the
// native controller default state branch; their MOVIE names do not make them
// AVI requests. Keep Luke and unproved movie IDs under the existing policy.
func (s *AntonProgression) ReconnectSymbols() map[uint32]uint32 {
	if s == nil {
		return nil
	}
	result := make(map[uint32]uint32, len(s.program.symbols))
	for name, id := range s.program.symbols {
		// Actual Anton controller 01877E20 opens movie files for IDs
		// 121..125/128 even when the supplied value is zero. The separate
		// native reset sentinel already clears client symbol storage;
		// restoring an AVI instruction cannot be made inert by writing 0.
		// Anton 126/127 use the default state branch instead: preserve their
		// current values. Luke and unproved MOVIE IDs keep the old exclusion.
		if strings.HasPrefix(name, "MOVIE ") && (s.program.luke || id != 126 && id != 127) {
			continue
		}
		value := s.symbols[name]
		if name == "KILL ALL CHARACTER" || name == "KICK OUT CHARACTER" ||
			(s.program.luke && (name == "KICK LUKE CHARACTER" || name == "1PHASE INIT")) ||
			name == "1PHASE FAIL" || name == "1PHASE SUCCESS" || name == "2PHASE INIT" {
			value = 0
		}
		result[id] = value
	}
	s.appendVolcanoNativeSymbol(result)
	return result
}
