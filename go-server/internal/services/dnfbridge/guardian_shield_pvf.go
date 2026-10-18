package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentGuardianShieldCatalogPath = "etc/character/knight/shieldwindownewdata.etc"

var (
	errGuardianShieldPVFUnavailable = errors.New("guardian shield PVF is unavailable")
	errGuardianShieldNotInCatalog   = errors.New("guardian shield item is not in the character grow catalog")
	errGuardianShieldItemInvalid    = errors.New("guardian shield item PVF is invalid")
)

type currentGuardianShieldPVFResolver struct {
	source         initialEquipmentTextSource
	equipmentPaths map[int64]string
	catalog        map[byte]map[uint32]currentGuardianShieldCatalogEntry
}

type currentGuardianShieldCatalogEntry struct {
	UnlockKind   alignedcmd.GuardianShieldUnlockKind
	ClearQuestID int64
	MinimumLevel int64
}

func (s *Service) newCurrentGuardianShieldResolver(ctx context.Context) (alignedcmd.GuardianShieldResolver, error) {
	if s == nil {
		return nil, errGuardianShieldPVFUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.preloadEquipmentStatIndex(ctx); err != nil {
		return nil, fmt.Errorf("%w: prepare equipment index: %v", errGuardianShieldPVFUnavailable, err)
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%w: open active archive: %v", errGuardianShieldPVFUnavailable, err)
	}
	catalogText, err := archive.ReadText(currentGuardianShieldCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", errGuardianShieldPVFUnavailable, currentGuardianShieldCatalogPath, err)
	}
	catalog, err := parseCurrentGuardianShieldCatalog(catalogText)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", errGuardianShieldPVFUnavailable, currentGuardianShieldCatalogPath, err)
	}
	s.equipmentStatsMu.Lock()
	paths := make(map[int64]string, len(s.equipmentStatPaths))
	for itemID, path := range s.equipmentStatPaths {
		paths[itemID] = path
	}
	s.equipmentStatsMu.Unlock()
	resolver := &currentGuardianShieldPVFResolver{
		source:         archive,
		equipmentPaths: paths,
		catalog:        catalog,
	}
	return resolver.Resolve, nil
}

func (r *currentGuardianShieldPVFResolver) Resolve(
	ctx context.Context,
	itemID uint32,
	growType byte,
) (alignedcmd.GuardianShieldResolution, error) {
	if r == nil || r.source == nil || itemID == 0 {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d", errGuardianShieldPVFUnavailable, itemID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return alignedcmd.GuardianShieldResolution{}, err
	}
	growType &= 0x0f
	items := r.catalog[growType]
	entry, found := items[itemID]
	if !found {
		// Newdata level rows use grow type zero and are shared by the two
		// current Guardian branches. Quest rows remain grow-specific.
		entry, found = r.catalog[0][itemID]
		if found && entry.UnlockKind != alignedcmd.GuardianShieldUnlockLevel {
			found = false
		}
	}
	if !found {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d grow=%d", errGuardianShieldNotInCatalog, itemID, growType)
	}
	refPath := strings.TrimSpace(r.equipmentPaths[int64(itemID)])
	if refPath == "" {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d is absent from equipment.lst", errGuardianShieldItemInvalid, itemID)
	}
	text, actualPath, err := readInitialPVFText(r.source, initialPVFPath("equipment", refPath), refPath)
	if err != nil {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d path=%q: %v", errGuardianShieldItemInvalid, itemID, refPath, err)
	}
	document, err := dnfpvf.Parse(actualPath, text)
	if err != nil {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d path=%q: %v", errGuardianShieldItemInvalid, itemID, actualPath, err)
	}
	equipmentType, typeFound := document.Text("equipment type")
	if !typeFound || normalizeEquipmentPlacementPVFType(equipmentType) != "[support weapon]" {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d equipment_type=%q", errGuardianShieldItemInvalid, itemID, equipmentType)
	}
	group, groupFound := document.Text("item group name")
	if !groupFound || normalizeGuardianShieldPVFText(group) != "shield" {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d item_group=%q", errGuardianShieldItemInvalid, itemID, group)
	}
	usableKnight := false
	for _, value := range document.Texts("usable job") {
		if normalizeGuardianShieldPVFText(value) == "[knight]" {
			usableKnight = true
			break
		}
	}
	if !usableKnight {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d lacks usable [knight]", errGuardianShieldItemInvalid, itemID)
	}
	cleanPath := cleanInitialPVFPath(actualPath)
	normalizedPath := "/" + strings.Trim(strings.ToLower(cleanPath), "/") + "/"
	if !strings.Contains(normalizedPath, "/equipment/character/knight/weapon/shield/") {
		return alignedcmd.GuardianShieldResolution{}, fmt.Errorf("%w: item=%d path=%q is outside knight shield family", errGuardianShieldItemInvalid, itemID, cleanPath)
	}
	return alignedcmd.GuardianShieldResolution{
		ItemID:        itemID,
		PVFPath:       cleanPath,
		EquipmentType: "[support weapon]",
		UnlockKind:    entry.UnlockKind,
		ClearQuestID:  entry.ClearQuestID,
		MinimumLevel:  entry.MinimumLevel,
	}, nil
}

func parseCurrentGuardianShieldCatalog(text string) (map[byte]map[uint32]currentGuardianShieldCatalogEntry, error) {
	document, err := dnfpvf.Parse(currentGuardianShieldCatalogPath, text)
	if err != nil {
		return nil, err
	}
	catalog := make(map[byte]map[uint32]currentGuardianShieldCatalogEntry, 3)
	insideShield := false
	var itemID int64
	var growType int64
	var unlockKind alignedcmd.GuardianShieldUnlockKind
	var clearQuestID int64
	var minimumLevel int64
	for _, section := range document.Sections {
		name := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(section.Name)), " "))
		switch name {
		case "shield":
			if insideShield {
				return nil, fmt.Errorf("nested [shield] section")
			}
			insideShield = true
			itemID = 0
			growType = 0
			unlockKind = alignedcmd.GuardianShieldUnlockUnknown
			clearQuestID = 0
			minimumLevel = 0
		case "/shield":
			// The current shieldwindownewdata contains one redundant closing tag.
			// The client accepts it; ignore only an unmatched close while keeping
			// every opened entry strict about all condition-specific fields.
			if !insideShield {
				continue
			}
			validAuthorization :=
				(unlockKind == alignedcmd.GuardianShieldUnlockQuest &&
					(growType == 1 || growType == 2) && clearQuestID > 0) ||
					(unlockKind == alignedcmd.GuardianShieldUnlockLevel &&
						growType == 0 && minimumLevel > 0)
			if itemID <= 0 || itemID > int64(^uint32(0)) || !validAuthorization {
				return nil, fmt.Errorf(
					"invalid shield entry item=%d grow=%d unlock=%d clear_quest=%d minimum_level=%d open=%t",
					itemID,
					growType,
					unlockKind,
					clearQuestID,
					minimumLevel,
					insideShield,
				)
			}
			grow := byte(growType)
			if catalog[grow] == nil {
				catalog[grow] = make(map[uint32]currentGuardianShieldCatalogEntry)
			}
			id := uint32(itemID)
			if _, duplicate := catalog[grow][id]; duplicate {
				return nil, fmt.Errorf("duplicate shield item=%d grow=%d", id, grow)
			}
			catalog[grow][id] = currentGuardianShieldCatalogEntry{
				UnlockKind:   unlockKind,
				ClearQuestID: clearQuestID,
				MinimumLevel: minimumLevel,
			}
			insideShield = false
		case "item index":
			if insideShield {
				itemID = firstCurrentGuardianShieldSectionInt(document, section)
			}
		case "grow type":
			if insideShield {
				growType = firstCurrentGuardianShieldSectionInt(document, section)
			}
		case "clear quest index":
			if insideShield {
				clearQuestID = firstCurrentGuardianShieldSectionInt(document, section)
			}
		case "get shield level":
			if insideShield {
				minimumLevel = firstCurrentGuardianShieldSectionInt(document, section)
			}
		case "get condition":
			if insideShield {
				switch normalizeGuardianShieldPVFText(firstCurrentGuardianShieldSectionText(document, section)) {
				case "quest":
					unlockKind = alignedcmd.GuardianShieldUnlockQuest
				case "level":
					unlockKind = alignedcmd.GuardianShieldUnlockLevel
				default:
					unlockKind = alignedcmd.GuardianShieldUnlockUnknown
				}
			}
		}
	}
	if insideShield || len(catalog[0]) == 0 || len(catalog[1]) == 0 || len(catalog[2]) == 0 {
		return nil, fmt.Errorf(
			"incomplete shield catalog open=%t shared_level=%d grow1_quest=%d grow2_quest=%d",
			insideShield,
			len(catalog[0]),
			len(catalog[1]),
			len(catalog[2]),
		)
	}
	return catalog, nil
}

func firstCurrentGuardianShieldSectionText(document *dnfpvf.Document, section dnfpvf.Section) string {
	if document == nil || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return ""
	}
	for _, token := range document.Tokens[section.Start:section.End] {
		if token.Kind == dnfpvf.TokenString || token.Kind == dnfpvf.TokenIdent {
			return token.Value
		}
	}
	return ""
}

func firstCurrentGuardianShieldSectionInt(document *dnfpvf.Document, section dnfpvf.Section) int64 {
	if document == nil || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return 0
	}
	for _, token := range document.Tokens[section.Start:section.End] {
		if token.Kind == dnfpvf.TokenInt {
			return token.Int
		}
	}
	return 0
}

func normalizeGuardianShieldPVFText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(value), "`\"'")), " "))
}

func currentMoveTouchesGuardianShieldSpace(body []byte) bool {
	if len(body) < 12 {
		return false
	}
	return body[0] == 33 || body[0] == 34 || body[11] == 33 || body[11] == 34
}
