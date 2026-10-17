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
// legacy u16 seed table. Only fresh Creator configurations use this PVF bank.
func (s *Service) creatorDefaultConfig(ctx context.Context) ([]byte, error) {
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
	return parseCreatorDefaultConfig(text)
}

func parseCreatorDefaultConfig(text string) ([]byte, error) {
	doc, err := dnfpvf.Parse(creatorDefaultConfigPVFPath, text)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	seen := map[string]bool{}
	creator := false
	for _, section := range doc.Sections {
		if strings.Trim(section.Name, "[] ") != "key" {
			continue
		}
		v := doc.Tokens[section.Start:section.End]
		if len(v) != 6 {
			return nil, fmt.Errorf("creator hotkey key row has %d fields", len(v))
		}
		if v[5].Value != "CommonType" && v[5].Value != "CreatorType" {
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
		if value == -1 {
			value = 0x86
		}
		if value < 0 || value > 0xff {
			return nil, fmt.Errorf("creator hotkey key value invalid")
		}
		if v[5].Value == "CreatorType" {
			creator = true
		}
		out.WriteString(id)
		out.WriteByte('\t')
		out.WriteString(strconv.FormatInt(value, 10))
		out.WriteByte('\n')
	}
	if !creator || !seen["I01"] || !seen["I02"] || !seen["I03"] || !seen["I04"] || !seen["I06"] || out.Len() == 0 || out.Len() >= 0x578 {
		return nil, fmt.Errorf("creator hotkey configuration incomplete or too large")
	}
	return []byte(out.String()), nil
}
