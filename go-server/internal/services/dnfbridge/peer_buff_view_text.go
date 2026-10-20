package dnfbridge

import (
	"fmt"
	"strings"
	"unicode"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const peerBuffViewMagic = "DNFBUF01"

// A bounded, read-only display appendix for the compatibility DLL. Native
// mode1 readers finish before this appendix. It is only attached to the narrow
// peer Buff carrier, inside the existing online/same-area ownership checks.
func (r *csharpLegacyUserInfoReader) writePeerBuffViewText(w *packetWriter, targetID uint16) {
	end := r.beginNativeBuffOwnedSerialization()
	defer end()
	state, rows, err := r.nativeBuffSwapUserInfoRows()
	if err != nil {
		return
	}
	name := ""
	skillName := fmt.Sprintf("技能 %d", state.Skill)
	base := byte(0)
	if projection, loadErr := r.loadNativeBuffOwnedProjection(); loadErr == nil {
		character := projection.Owner
		name = peerBuffDisplayName(character.Name)
		if r.service.skillCatalog != nil {
			if definition, found := r.service.skillCatalog.Find(byte(numericCharacterStat(character.Job)), state.Skill); found {
				skillName = peerBuffDisplayName(definition.Name)
			}
		}
	}
	for _, entry := range peerSkillPageEntries(r.loadPeerSkillPageSkills(r.characterID)) {
		if entry.SkillID == state.Skill {
			base = entry.Level
			break
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s 的 Buff 登记\r\n\r\n", name)
	if state.Skill == 0 {
		text.WriteString("尚未选择 Buff 技能\r\n")
	} else {
		fmt.Fprintf(&text, "%s · 基础 Lv%d\r\n", skillName, base)
	}
	fmt.Fprintf(&text, "已登记 %d 件装备\r\n\r\n", len(rows))
	for _, row := range rows {
		itemName := fmt.Sprintf("装备 %d", row.binding.ItemID)
		if catalog, catalogErr := r.service.currentPVFItemCatalog(); catalogErr == nil && catalog != nil {
			if definition, resolveErr := catalog.ResolveItem(uint32(row.binding.ItemID)); resolveErr == nil {
				if path, source, readErr := readDungeonDropText(catalog.source, string(definition.Kind), definition.PVFPath); readErr == nil {
					if document, parseErr := dnfpvf.Parse(path, source); parseErr == nil {
						if value, found := document.Text("name"); found && value != "" {
							itemName = peerBuffDisplayName(value)
						}
					}
				}
			}
		}
		fmt.Fprintf(&text, "  %s\r\n", itemName)
	}
	if len(rows) == 0 {
		text.WriteString("暂无登记装备\r\n")
	}
	text.WriteString("\r\n仅供查看")
	body := []byte(text.String())
	if len(body) > 8192 {
		return
	}
	w.writeBytes(body)
	w.writeBytes([]byte(peerBuffViewMagic))
	w.writeUint32(uint32(targetID))
	w.writeUint32(uint32(len(body)))
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
