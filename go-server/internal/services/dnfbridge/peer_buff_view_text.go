package dnfbridge

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const peerBuffViewMagic = "DNFBUF01"
const peerBuffViewTextLimit = 8192

type peerBuffViewTextItem struct {
	ItemID int64
	Name   string
}

type peerBuffViewTextSkill struct {
	SkillID uint16
	Name    string
	Level   byte
	Items   []peerBuffViewTextItem
	Omitted int
}

type peerBuffViewTextDocument struct {
	CharacterName string
	CurrentSkill  uint16
	CurrentName   string
	Skills        []peerBuffViewTextSkill
}

// A bounded, read-only display appendix for a compatibility sidecar. The
// matching native EXE has no DNFBUF01 marker and does not render this section.
// Native mode1 readers finish before it. It is only attached to the narrow peer
// Buff carrier, inside the existing online/same-area ownership checks.
func (r *csharpLegacyUserInfoReader) writePeerBuffViewText(w *packetWriter, targetID uint16) {
	end := r.beginNativeBuffOwnedSerialization()
	defer end()
	state, _, err := r.nativeBuffSwapUserInfoRows()
	if err != nil {
		return
	}
	name := ""
	job := byte(0)
	var registered []buffswap.OwnedRegisteredSkill
	if projection, loadErr := r.loadNativeBuffOwnedProjection(); loadErr == nil {
		character := projection.Owner
		name = peerBuffDisplayName(character.Name)
		job = byte(numericCharacterStat(character.Job))
		registered = projection.RegisteredSkills
	}
	entries := peerBuffRegisteredSkillEntries(
		registered,
		peerSkillPageEntries(r.loadPeerSkillPageSkills(r.characterID)),
	)
	document := peerBuffViewTextDocument{
		CharacterName: name,
		CurrentSkill:  state.Skill,
		CurrentName:   r.peerBuffSkillName(job, state.Skill),
	}
	if len(entries) != 0 {
		bySkill := make(map[uint16]buffswap.OwnedRegisteredSkill, len(registered))
		for _, registration := range registered {
			bySkill[registration.Skill] = registration
		}
		itemNames := make(map[int64]string)
		for _, entry := range entries {
			skill := peerBuffViewTextSkill{
				SkillID: entry.SkillID,
				Name:    r.peerBuffSkillName(job, entry.SkillID),
				Level:   entry.Level,
			}
			registration := bySkill[entry.SkillID]
			skill.Omitted = registration.Omitted
			for slot := byte(0); slot < buffswap.SlotCount; slot++ {
				item, found := registration.Items[slot]
				if !found {
					continue
				}
				itemName, found := itemNames[item.Binding.ItemID]
				if !found {
					itemName = r.peerBuffItemName(item.Binding.ItemID)
					itemNames[item.Binding.ItemID] = itemName
				}
				skill.Items = append(skill.Items, peerBuffViewTextItem{ItemID: item.Binding.ItemID, Name: itemName})
			}
			document.Skills = append(document.Skills, skill)
		}
	}
	body := renderPeerBuffViewText(document)
	w.writeBytes(body)
	w.writeBytes([]byte(peerBuffViewMagic))
	w.writeUint32(uint32(targetID))
	w.writeUint32(uint32(len(body)))
}

func renderPeerBuffViewText(document peerBuffViewTextDocument) []byte {
	const footer = "\r\n仅供查看"
	var text strings.Builder
	fmt.Fprintf(&text, "%s 的 Buff 登记\r\n\r\n", document.CharacterName)
	if document.CurrentSkill == 0 {
		text.WriteString("当前未选择 Buff 技能\r\n")
	}
	if len(document.Skills) == 0 {
		if document.CurrentSkill != 0 {
			fmt.Fprintf(&text, "%s · 基础 Lv0\r\n", document.CurrentName)
		} else {
			text.WriteString("尚无已登记 Buff 技能\r\n")
		}
	} else {
		fmt.Fprintf(&text, "可查看 Buff 技能 %d 个\r\n", len(document.Skills))
		for _, skill := range document.Skills {
			marker := ""
			if skill.SkillID == document.CurrentSkill {
				marker = "（当前）"
			}
			fmt.Fprintf(&text, "  %s · 基础 Lv%d%s\r\n", skill.Name, skill.Level, marker)
			if len(skill.Items) == 0 {
				text.WriteString("    暂无登记装备\r\n")
			} else {
				fmt.Fprintf(&text, "    已登记 %d 件装备\r\n", len(skill.Items))
				for _, item := range skill.Items {
					fmt.Fprintf(&text, "      %s\r\n", item.Name)
				}
			}
			if skill.Omitted > 0 {
				fmt.Fprintf(&text, "    另有 %d 件登记装备当前不可定位\r\n", skill.Omitted)
			}
		}
	}
	text.WriteString(footer)
	body := []byte(text.String())
	if len(body) <= peerBuffViewTextLimit {
		return body
	}
	return renderPeerBuffCompactViewText(document, footer)
}

func renderPeerBuffCompactViewText(document peerBuffViewTextDocument, footer string) []byte {
	if len(document.Skills) == 0 {
		return renderPeerBuffSkillIndex(document)
	}
	header := fmt.Sprintf("%s Buff compact %d\r\n", peerBuffCompactName(document.CharacterName), len(document.Skills))
	available := peerBuffViewTextLimit - len(header) - len(footer)
	perSkill := available / len(document.Skills)
	var text strings.Builder
	text.Grow(peerBuffViewTextLimit)
	text.WriteString(header)
	for _, skill := range document.Skills {
		marker := ""
		if skill.SkillID == document.CurrentSkill {
			marker = "*"
		}
		line := fmt.Sprintf("S%d/L%d%s:", skill.SkillID, skill.Level, marker)
		contentBudget := perSkill - 2 // CRLF
		shown := 0
		for index, item := range skill.Items {
			token := strconv.FormatInt(item.ItemID, 10)
			if shown != 0 {
				token = "," + token
			}
			remaining := len(skill.Items) - index - 1
			reserve := 0
			if remaining > 0 {
				reserve += len(",+") + len(strconv.Itoa(remaining))
			}
			if skill.Omitted > 0 {
				reserve += len(",!") + len(strconv.Itoa(skill.Omitted))
			}
			if len(line)+len(token)+reserve > contentBudget {
				break
			}
			line += token
			shown++
		}
		if len(skill.Items) == 0 {
			line += "-"
		} else {
			remaining := len(skill.Items) - shown
			if shown == 0 {
				line += "+" + strconv.Itoa(remaining)
			} else if remaining > 0 {
				line += ",+" + strconv.Itoa(remaining)
			}
		}
		if skill.Omitted > 0 {
			line += ",!" + strconv.Itoa(skill.Omitted)
		}
		if len(line) > contentBudget {
			line = fmt.Sprintf("S%d/L%d%s", skill.SkillID, skill.Level, marker)
		}
		text.WriteString(line)
		text.WriteString("\r\n")
	}
	text.WriteString(footer)
	body := []byte(text.String())
	if len(body) <= peerBuffViewTextLimit {
		return body
	}
	return renderPeerBuffSkillIndex(document)
}

func renderPeerBuffSkillIndex(document peerBuffViewTextDocument) []byte {
	var text strings.Builder
	fmt.Fprintf(&text, "%s Buff skills\r\n", peerBuffCompactName(document.CharacterName))
	for _, skill := range document.Skills {
		marker := ""
		if skill.SkillID == document.CurrentSkill {
			marker = "*"
		}
		fmt.Fprintf(&text, "S%d/L%d%s\r\n", skill.SkillID, skill.Level, marker)
	}
	text.WriteString("\r\n仅供查看")
	return []byte(text.String())
}

func peerBuffCompactName(value string) string {
	runes := []rune(peerBuffDisplayName(value))
	if len(runes) > 24 {
		runes = runes[:24]
	}
	return string(runes)
}

func (r *csharpLegacyUserInfoReader) peerBuffItemName(itemID int64) string {
	name := fmt.Sprintf("装备 %d", itemID)
	if r == nil || r.service == nil {
		return name
	}
	catalog, catalogErr := r.service.currentPVFItemCatalog()
	if catalogErr != nil || catalog == nil {
		return name
	}
	definition, resolveErr := catalog.ResolveItem(uint32(itemID))
	if resolveErr != nil {
		return name
	}
	path, source, readErr := readDungeonDropText(catalog.source, string(definition.Kind), definition.PVFPath)
	if readErr != nil {
		return name
	}
	document, parseErr := dnfpvf.Parse(path, source)
	if parseErr != nil {
		return name
	}
	if value, found := document.Text("name"); found && value != "" {
		return peerBuffDisplayName(value)
	}
	return name
}

func (r *csharpLegacyUserInfoReader) peerBuffSkillName(job byte, skill uint16) string {
	name := fmt.Sprintf("技能 %d", skill)
	if r != nil && r.service != nil && r.service.skillCatalog != nil {
		if definition, found := r.service.skillCatalog.Find(job, skill); found {
			name = peerBuffDisplayName(definition.Name)
		}
	}
	return name
}

func peerBuffDisplayName(value string) string {
	runes := []rune(strings.Trim(value, "` \r\n\t"))
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, string(runes))
}
