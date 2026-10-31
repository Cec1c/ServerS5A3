package dnfbridge

import (
	"fmt"
	"path"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type currentGiftBoxHistoryRow struct {
	kind    string
	history []uint32
}

// The local manifest is not referenced by client lists or item scripts. Old
// archives have no manifest and retain their existing behavior.
func loadCurrentGiftBoxExpirationHistory(source dnfpvf.Source) (map[string]currentGiftBoxHistoryRow, error) {
	// Only archives with an explicit file inventory can attest this optional
	// local extension. A failed read of an existing manifest is always an error.
	inventory, ok := source.(interface {
		FindFile(string) (platformpvf.File, bool)
	})
	if !ok {
		return nil, nil
	}
	if _, exists := inventory.FindFile(itemexpiration.GiftBoxHistoryPath); !exists {
		return nil, nil
	}
	text, err := source.ReadText(itemexpiration.GiftBoxHistoryPath)
	if err != nil {
		return nil, fmt.Errorf("read giftbox provenance: %w", err)
	}
	doc, err := dnfpvf.Parse(itemexpiration.GiftBoxHistoryPath, text)
	if err != nil {
		return nil, err
	}
	result := map[string]currentGiftBoxHistoryRow{}
	version, target, inEntry := false, false, false
	for _, section := range doc.Sections {
		if inEntry && section.Name != "/entry" {
			return nil, fmt.Errorf("unclosed giftbox provenance entry")
		}
		values := doc.Tokens[section.Start:section.End]
		switch section.Name {
		case "version":
			if version || target || len(result) != 0 || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int != 1 {
				return nil, fmt.Errorf("invalid giftbox provenance version")
			}
			version = true
		case "target":
			if !version || target || len(result) != 0 || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int != int64(itemexpiration.GiftBoxHistoryDeadline) {
				return nil, fmt.Errorf("invalid giftbox provenance target")
			}
			target = true
		case "entry":
			if !target || len(values) < 3 || len(values) > 18 || values[0].Kind != dnfpvf.TokenString || values[1].Kind != dnfpvf.TokenString {
				return nil, fmt.Errorf("invalid giftbox provenance entry")
			}
			key, kind := values[0].Value, values[1].Value
			if key != strings.ToLower(key) || path.Clean(key) != key || !strings.HasPrefix(key, "stackable/") || !strings.HasSuffix(key, ".stk") || !itemexpiration.IsGiftBoxPath(key, kind) {
				return nil, fmt.Errorf("invalid giftbox provenance identity")
			}
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("duplicate giftbox provenance path")
			}
			row := currentGiftBoxHistoryRow{kind: kind}
			seen := map[uint32]bool{}
			for _, v := range values[2:] {
				if v.Kind != dnfpvf.TokenInt || v.Int <= 0 || v.Int >= int64(itemexpiration.GiftBoxHistoryDeadline) || seen[uint32(v.Int)] {
					return nil, fmt.Errorf("invalid giftbox provenance deadline")
				}
				seen[uint32(v.Int)] = true
				row.history = append(row.history, uint32(v.Int))
			}
			result[key] = row
			inEntry = true
		case "/entry":
			if !inEntry || len(values) != 0 {
				return nil, fmt.Errorf("invalid giftbox provenance closing entry")
			}
			inEntry = false
		default:
			return nil, fmt.Errorf("unknown giftbox provenance section %q", section.Name)
		}
	}
	if !version || !target || inEntry || len(result) == 0 {
		return nil, fmt.Errorf("incomplete giftbox provenance")
	}
	return result, nil
}

func currentGiftBoxInstanceHasIndependentExpiration(extra map[string]string) bool {
	source := strings.ToLower(strings.TrimSpace(extra["expiration_source"]))
	if source != "" && source != "runtime_pvf" && source != "runtime_pvf_giftbox_history" {
		return true
	}
	if currentGMExpirationOverrideMarked(extra) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(extra["source"]), "pvf_rental") {
		return true
	}
	for _, key := range []string{"rental", "rental_item", "rental_expire_time", "rental_duration_hours", "gm_expire_days", "usable_period_days", "cera_shop_duration_days", "daily_delete_service_day", itemexpiration.DailyDeleteDeadlineKey} {
		if v := strings.TrimSpace(extra[key]); v != "" && v != "0" {
			return true
		}
	}
	return false
}

func currentGiftBoxInstanceNeedsRenewal(definition dungeonDropItemDefinition, instance uint32) bool {
	return definition.Kind == dungeonDropItemStackable && itemexpiration.IsGiftBoxPath(definition.PVFPath, definition.StackableType) && !definition.DailyDeleteItem && definition.UsablePeriodDays == 0 && itemexpiration.GiftBoxDeadlineMatches(instance, currentPVFExpirationUnix(definition.ExpirationDate), definition.GiftBoxExpirationHistory)
}

func currentGiftBoxHistoryExpiration(definition dungeonDropItemDefinition, instance uint32, protected bool) time.Time {
	if !protected && privateExperienceInstanceNeedsRenewal(definition, instance) {
		return definition.ExpirationDate
	}
	// The exact festival manifest also renews the exact 50 audited festival definitions,
	// including coupons and stones which are not gift containers. The manifest
	// matches item, path, previous absolute date and current PVF deadline.
	if !protected && privateFestivalInstanceNeedsRenewal(definition, instance) {
		return definition.ExpirationDate
	}
	if !protected && currentGiftBoxInstanceNeedsRenewal(definition, instance) {
		return definition.ExpirationDate
	}
	return time.Time{}
}
