package dnfbridge

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func currentSocketEquipmentRule(catalog *pvfDungeonDropCatalog, itemID int64) (currentEquipmentPlacementRule, error) {
	if catalog == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return currentEquipmentPlacementRule{}, errCurrentSocketPVFInvalid
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return currentEquipmentPlacementRule{}, err
	}
	if definition.Kind != dungeonDropItemEquipment {
		return currentEquipmentPlacementRule{}, fmt.Errorf("%w: item=%d kind=%s", errCurrentSocketTargetKindMismatch, itemID, definition.Kind)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return currentEquipmentPlacementRule{}, fmt.Errorf("%w: item=%d path=%s: %v", errCurrentSocketPVFInvalid, itemID, definition.PVFPath, err)
	}
	pvfType, ok := document.Text("equipment type")
	if !ok {
		return currentEquipmentPlacementRule{}, fmt.Errorf("%w: item=%d path=%s missing equipment type", errCurrentSocketPVFInvalid, itemID, definition.PVFPath)
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(pvfType)
	if !ok {
		return currentEquipmentPlacementRule{}, fmt.Errorf("%w: item=%d type=%q", errCurrentSocketPVFInvalid, itemID, pvfType)
	}
	return rule, nil
}

func currentEquipmentSocketOpenMaterialRule(catalog *pvfDungeonDropCatalog, itemID int64) error {
	if catalog == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return errCurrentSocketMaterialInvalid
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return fmt.Errorf("%w: item=%d: %v", errCurrentSocketMaterialInvalid, itemID, err)
	}
	if definition.Kind != dungeonDropItemStackable || !strings.EqualFold(strings.TrimSpace(definition.PVFPath), currentEquipmentSocketOpenToolPVFPath) {
		return fmt.Errorf("%w: item=%d kind=%s path=%s", errCurrentSocketMaterialInvalid, itemID, definition.Kind, definition.PVFPath)
	}
	return nil
}

func currentSocketAvatarSocketTypes(catalog *pvfDungeonDropCatalog, itemID int64) ([]byte, error) {
	if catalog == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return nil, errCurrentSocketPVFInvalid
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return nil, err
	}
	if definition.Kind != dungeonDropItemEquipment {
		return nil, fmt.Errorf("%w: avatar item=%d kind=%s", errCurrentSocketTargetKindMismatch, itemID, definition.Kind)
	}
	text, err := catalog.source.ReadText(definition.PVFPath)
	if err != nil {
		return nil, fmt.Errorf("%w: item=%d path=%s: %v", errCurrentSocketPVFInvalid, itemID, definition.PVFPath, err)
	}
	socketTypes := currentParseAvatarSocketTypes(text)
	if len(socketTypes) == 0 {
		return nil, nil
	}
	return socketTypes, nil
}

func currentSocketEmblemType(catalog *pvfDungeonDropCatalog, itemID int64) byte {
	if catalog == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return 0
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil || definition.Kind != dungeonDropItemStackable {
		return 0
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return 0
	}
	return currentMapAvatarEmblemTargetTypes(document.Texts("avatar emblem target type"))
}

func currentParseAvatarSocketTypes(text string) []byte {
	socketTypes := currentParseAvatarSocketTypesFromSection(currentExtractPVFTextSection(text, "[avatar type select]", "[/avatar type select]"), currentAvatarSocketCount)
	if len(socketTypes) > 0 {
		return socketTypes
	}
	return currentParseAvatarDefaultSocketTypes(text)
}

// currentParseAvatarDefaultSocketTypes resolves only the PVF-declared sockets
// that an avatar owns immediately when it is created.  [avatar type select]
// describes the types available to the manual socket-opening flow and must not
// make an ordinary avatar arrive pre-opened.
func currentParseAvatarDefaultSocketTypes(text string) []byte {
	return currentParseAvatarSocketTypesFromSection(
		currentExtractPVFTextSection(text, "[emblem socket default]", "[/emblem socket default]"),
		currentResolveAvatarSocketNum(text),
	)
}

// currentAvatarGrantUsesCreationSockets decides whether a granted avatar must
// arrive with its PVF-declared creation-time sockets already open.
//
// The runtime Script.pvf distinguishes the two avatar shapes by declaration, not
// by slot:
//   - auras ([aurora avatar]) carry [emblem socket default] (two M sockets);
//   - skins ([skin avatar]) carry [avatar emblem socket num] 2 and list those two
//     codes at the end of [avatar type select] ("2 `[M socket]` `[M socket]`");
//   - ordinary parts (hat/hair/face/coat/pants/shoes/breast/waist) declare
//     neither, and their [avatar type select] list only enumerates what the
//     manual socket-opening flow may drill.
//
// So the presence of a creation-time declaration is the authority; an ordinary
// part must never be pre-opened even when it lists socket types.
func currentAvatarGrantUsesCreationSockets(rule currentEquipmentPlacementRule, socketTypes []byte) bool {
	if rule.class != currentEquipmentPlacementClassAvatar || len(socketTypes) == 0 {
		return false
	}
	return true
}

// currentParseAvatarCreationSocketTypes resolves the sockets an avatar owns from
// the moment it is created, accepting both runtime-PVF shapes above.  It returns
// nil for an avatar that declares none, including an ordinary part whose
// [avatar type select] list exists only for the manual opening flow.
func currentParseAvatarCreationSocketTypes(text string) []byte {
	if defaults := currentParseAvatarDefaultSocketTypes(text); len(defaults) > 0 {
		return defaults
	}
	count := currentResolveDeclaredAvatarSocketNum(text)
	if count <= 0 {
		return nil
	}
	// [avatar type select] 的尾部就是这座时装的孔位类型（皮肤是 2 × [M socket]）；
	// 声明了几个孔就取前几个。
	return currentParseAvatarSocketTypesFromSection(
		currentExtractPVFTextSection(text, "[avatar type select]", "[/avatar type select]"),
		count,
	)
}

// currentResolveDeclaredAvatarSocketNum returns the [avatar emblem socket num]
// an avatar declares, or zero when the tag is absent.  Absence means "no
// creation-time sockets": unlike currentResolveAvatarSocketNum it must not fall
// back to the maximum count, or every ordinary avatar part would look socketed.
func currentResolveDeclaredAvatarSocketNum(text string) int {
	lower := strings.ToLower(text)
	const tag = "[avatar emblem socket num]"
	start := strings.Index(lower, tag)
	if start < 0 {
		return 0
	}
	start += len(tag)
	section := text[start:]
	if end := strings.Index(section, "["); end >= 0 {
		section = section[:end]
	}
	for _, token := range strings.Fields(section) {
		value, err := strconv.Atoi(token)
		if err != nil {
			continue
		}
		if value <= 0 {
			return 0
		}
		if value > currentAvatarSocketCount {
			return currentAvatarSocketCount
		}
		return value
	}
	return 0
}

func currentParseAvatarSocketTypesFromSection(section string, maxCount int) []byte {
	if strings.TrimSpace(section) == "" || maxCount <= 0 {
		return nil
	}
	if maxCount > currentAvatarSocketCount {
		maxCount = currentAvatarSocketCount
	}
	out := make([]byte, 0, maxCount)
	for _, match := range currentAvatarSocketRE.FindAllStringSubmatch(section, -1) {
		if len(match) < 2 || match[1] == "" {
			continue
		}
		if socketType, ok := currentMapAvatarSocketCode(rune(match[1][0])); ok {
			out = append(out, socketType)
			if len(out) >= maxCount {
				break
			}
		}
	}
	return out
}

func currentExtractPVFTextSection(text, startTag, endTag string) string {
	lower := strings.ToLower(text)
	start := strings.Index(lower, strings.ToLower(startTag))
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(lower[start:], strings.ToLower(endTag))
	if end < 0 {
		return text[start:]
	}
	return text[start : start+end]
}

func currentResolveAvatarSocketNum(text string) int {
	lower := strings.ToLower(text)
	tag := "[avatar emblem socket num]"
	start := strings.Index(lower, tag)
	if start < 0 {
		return currentAvatarSocketCount
	}
	start += len(tag)
	end := strings.Index(text[start:], "[")
	section := text[start:]
	if end >= 0 {
		section = text[start : start+end]
	}
	for _, token := range strings.Fields(section) {
		value, err := strconv.Atoi(token)
		if err != nil {
			continue
		}
		if value < 0 {
			return 0
		}
		if value > currentAvatarSocketCount {
			return currentAvatarSocketCount
		}
		return value
	}
	return currentAvatarSocketCount
}

func currentMapAvatarEmblemTargetTypes(values []string) byte {
	var out byte
	for _, value := range values {
		for _, match := range currentAvatarSocketRE.FindAllStringSubmatch(value, -1) {
			if len(match) < 2 || match[1] == "" {
				continue
			}
			if socketType, ok := currentMapAvatarSocketCode(rune(match[1][0])); ok {
				out |= socketType
			}
		}
	}
	return out
}

// currentAvatarCreationSocketTypes returns the creation-time sockets a granted
// avatar must already own, or nil when the item is not a born-socketed avatar.
// A missing or unreadable PVF document is not fatal here: the avatar is still
// granted, it simply arrives closed and the manual socket-opening flow stays the
// only way to open it.
func currentAvatarCreationSocketTypes(source dnfpvf.Source, pvfPath string, rule currentEquipmentPlacementRule) []byte {
	if source == nil || pvfPath == "" || rule.class != currentEquipmentPlacementClassAvatar {
		return nil
	}
	text, err := source.ReadText(pvfPath)
	if err != nil {
		return nil
	}
	socketTypes := currentParseAvatarCreationSocketTypes(text)
	if !currentAvatarGrantUsesCreationSockets(rule, socketTypes) {
		return nil
	}
	return socketTypes
}

func currentMapAvatarSocketCode(code rune) (byte, bool) {
	switch code {
	case 'A', 'a':
		return 0x01, true
	case 'B', 'b':
		return 0x02, true
	case 'C', 'c':
		return 0x04, true
	case 'D', 'd':
		return 0x08, true
	case 'S', 's':
		return 0x10, true
	case 'M', 'm':
		return 0xEF, true
	default:
		return 0, false
	}
}
