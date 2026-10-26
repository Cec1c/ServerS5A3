package dnfbridge

import (
	"fmt"
	"math"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The ordinary v101 equipment constructor copies PVF [grade] (definition+8)
// into object+0x54. vtable+0x28 returns object+8, and the fee caller reads
// state+0x4c. Do not substitute minimum level or add a rarity offset again.
func resolveNativeUpgradeEquipmentCost(table *currentUpgradeTable, doc *dnfpvf.Document, level int) (int64, error) {
	return resolveNativeEquipmentGoldCost(table, doc, level, nativeUpgradeGoldCost)
}

func resolveNativeEquipmentGoldCost(table *currentUpgradeTable, doc *dnfpvf.Document, level int, calculate func(*currentUpgradeTable, int, int, int, int) (int64, error)) (int64, error) {
	// The candidate supports only ordinary static equipment metadata. These
	// sections need native semantics that are not implemented here; their
	// presence is not itself evidence that the ordinary fee is incorrect.
	// Section reports presence even for empty bodies and normalizes names.
	for _, name := range []string{"level linear ability", "upgrade cost discount", "custom upgrade type", "upgrade exclusive material"} {
		if _, present := doc.Section(name); present {
			return 0, fmt.Errorf("upgrade cost unsupported metadata %q", name)
		}
	}
	scalar := func(name string) (int, error) {
		values, found := doc.Section(name)
		if !found || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 || values[0].Int > math.MaxInt32 {
			return 0, fmt.Errorf("upgrade equipment %s must be one nonnegative integer", name)
		}
		return int(values[0].Int), nil
	}
	grade, err := scalar("grade")
	if err != nil {
		return 0, err
	}
	rarity, err := scalar("rarity")
	if err != nil {
		return 0, err
	}
	kindText, found := doc.Text("equipment type")
	rule, valid := currentEquipmentPlacementRuleForPVFType(kindText)
	if !found || !valid || rule.targetSlot < 12 || rule.targetSlot > 25 || rule.targetSlot == 13 {
		return 0, fmt.Errorf("unsupported upgrade equipment type %q", kindText)
	}
	for _, text := range doc.Texts("impossible contents") {
		if strings.Trim(text, "` []") == "upgrade" {
			return 0, fmt.Errorf("equipment forbids upgrade")
		}
	}
	return calculate(table, grade, rarity, int(rule.targetSlot), level)
}

// A quote remains optional until the caller explicitly installs this resolver.
// It is invoked by the owner inside its asset transaction, after target identity
// validation, and before RNG or writes. Portable items bypass it in the owner.
func (s *Service) alignedNativeUpgradeCostResolver() alignedcmd.UpgradeCostResolver {
	return func(mode string, itemID int64, level int, npcPresent bool, npcID uint16) (alignedcmd.UpgradeCostResolution, error) {
		result := alignedcmd.UpgradeCostResolution{}
		if mode != "reinforce" && mode != "amplify" {
			return result, nil
		}
		if itemID <= 0 || itemID > math.MaxUint32 {
			return result, fmt.Errorf("invalid upgrade item id")
		}
		table, err := s.currentUpgradeTableForMode(mode)
		if err != nil {
			return result, err
		}
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return result, err
		}
		definition, err := catalog.ResolveItem(uint32(itemID))
		if err != nil {
			return result, err
		}
		if definition.Kind != dungeonDropItemEquipment {
			return result, fmt.Errorf("upgrade target is not equipment")
		}
		doc, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if err != nil {
			return result, err
		}
		calculate := nativeUpgradeGoldCost
		if mode == "amplify" {
			calculate = nativeAmplifyGoldCost
		}
		cost, err := resolveNativeEquipmentGoldCost(table, doc, level, calculate)
		if err != nil {
			return result, err
		}
		result.Resolved, result.GoldCost = true, cost
		// NPC material conversion remains the separately verified reinforce path.
		if mode == "amplify" || !npcPresent || npcID == math.MaxUint16 {
			return result, nil
		}
		materialID, unitGold, converts, err := s.currentUpgradeNPCConversion(npcID)
		if err != nil {
			return alignedcmd.UpgradeCostResolution{}, err
		}
		if !converts {
			return result, nil
		}
		if unitGold <= 0 || materialID <= 0 {
			return alignedcmd.UpgradeCostResolution{}, fmt.Errorf("invalid NPC conversion rule")
		}
		// Quantity matches the native displayed price (ceil), not the click-side
		// availability precheck, which adds one even on exact multiples.
		count := cost / unitGold
		if cost%unitGold != 0 {
			count++
		}
		result.GoldCost = 0
		result.ConversionItemID, result.ConversionCount = materialID, count
		return result, nil
	}
}

// Default installs the verified NPC reinforcement conversion and ordinary
// amplification gold quote. Portable consumables bypass quotation in the owner.
// "candidate" additionally enables ordinary reinforcement gold quotation;
// other nonempty values retain the existing explicit opt-out behavior.
func (s *Service) candidateUpgradeCostResolver(session *gameSession) alignedcmd.UpgradeCostResolver {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_NATIVE_UPGRADE_COST"))) {
	case "candidate":
		base := s.alignedNativeUpgradeCostResolver()
		identity, _ := s.boundGameSessionCharacterSnapshot(session)
		return func(mode string, itemID int64, level int, present bool, npcID uint16) (alignedcmd.UpgradeCostResolution, error) {
			quote, err := base(mode, itemID, level, present, npcID)
			if err != nil {
				return quote, err
			}
			if quote.ConversionCount > 0 {
				if err := s.validateUpgradeNPCScene(identity, npcID); err != nil {
					return alignedcmd.UpgradeCostResolution{}, err
				}
			}
			return quote, nil
		}
	case "":
		identity, _ := s.boundGameSessionCharacterSnapshot(session)
		conversion := s.alignedUpgradeNPCConversionCostResolver(identity)
		base := s.alignedNativeUpgradeCostResolver()
		return func(mode string, itemID int64, level int, present bool, npcID uint16) (alignedcmd.UpgradeCostResolution, error) {
			if mode == "amplify" {
				return base(mode, itemID, level, present, npcID)
			}
			return conversion(mode, itemID, level, present, npcID)
		}
	}
	return nil
}
