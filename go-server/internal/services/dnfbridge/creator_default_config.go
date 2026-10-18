package dnfbridge

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const creatorDefaultConfigPVFPath = "clientonly/hotkeysystem_ver3.co"

var creatorConfigKeyID = regexp.MustCompile(`^[A-Z][0-9]{2}$`)

// The current 454 consumer parses key-name/tab/value/newline text, not the
// legacy u16 seed table. PVF values are Windows VKs; saved 454 text contains
// engine key codes, so only newly generated defaults need conversion.
func (s *Service) creatorDefaultConfig(ctx context.Context) ([]byte, error) {
	return s.nativeDefaultConfig(ctx, true)
}

func (s *Service) nativeDefaultConfig(ctx context.Context, creator bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.initialEquipmentMu.Lock()
	a, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("creator hotkey PVF unavailable")
	}
	text, _, err := readInitialPVFText(a, creatorDefaultConfigPVFPath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseNativeDefaultConfig(text, creator)
}

func parseCreatorDefaultConfig(text string) ([]byte, error) {
	return parseNativeDefaultConfig(text, true)
}

func parseNativeDefaultConfig(text string, creator bool) ([]byte, error) {
	doc, err := dnfpvf.Parse(creatorDefaultConfigPVFPath, text)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	seen := map[string]bool{}
	bank := "NormalType"
	if creator {
		bank = "CreatorType"
	}
	bankFound := false
	for _, section := range doc.Sections {
		if strings.Trim(section.Name, "[] ") != "key" {
			continue
		}
		v := doc.Tokens[section.Start:section.End]
		if len(v) != 6 {
			return nil, fmt.Errorf("creator hotkey key row has %d fields", len(v))
		}
		if v[5].Value != "CommonType" && v[5].Value != bank {
			continue
		}
		if v[1].Kind != dnfpvf.TokenString || !creatorConfigKeyID.MatchString(v[1].Value) || v[4].Kind != dnfpvf.TokenInt {
			return nil, fmt.Errorf("creator hotkey key row invalid")
		}
		id := v[1].Value
		if seen[id] {
			return nil, fmt.Errorf("creator hotkey duplicate key %s", id)
		}
		seen[id] = true
		value := v[4].Int
		if value < -1 || value > 0xff {
			return nil, fmt.Errorf("creator hotkey key value invalid")
		}
		value = int64(nativeHotkeyCodeFromVK(value))
		if v[5].Value == bank {
			bankFound = true
		}
		out.WriteString(id)
		out.WriteByte('\t')
		out.WriteString(strconv.FormatInt(value, 10))
		out.WriteByte('\n')
	}
	if !bankFound || !seen["I01"] || !seen["I02"] || !seen["I03"] || !seen["I04"] || !seen["I06"] || out.Len() == 0 || out.Len() >= 0x578 {
		return nil, fmt.Errorf("creator hotkey configuration incomplete or too large")
	}
	return []byte(out.String()), nil
}
