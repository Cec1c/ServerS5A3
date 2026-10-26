package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
)

const currentPVFDailyEquipmentGrantSource = "runtime_pvf_daily_equipment_grant"

// Only BuildNew of the existing PVF grant owner records this provenance.
// Loading a previously issued rental must neither invent its acquisition day
// nor reinterpret a GM/independent deadline as a daily deletion instruction.
func currentPVFDailyEquipmentGrantRecorded(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition) bool {
	if definition.Kind != dungeonDropItemEquipment || !definition.DailyDeleteItem ||
		stack.ItemID != int64(definition.ItemID) || definition.UsablePeriodDays > 0 ||
		stack.Extra["expiration_source"] != currentPVFDailyEquipmentGrantSource ||
		stack.Extra["pvf_path"] != definition.PVFPath || stack.ExpireAt.IsZero() {
		return false
	}
	deadline := stack.ExpireAt.Unix()
	if deadline <= 0 || deadline > math.MaxUint32 || stack.ExpireAt.Nanosecond() != 0 {
		return false
	}
	local := stack.ExpireAt.In(currentPVFExpirationDateLocation)
	if local.Hour() != 6 || local.Minute() != 0 || local.Second() != 0 {
		return false
	}
	for _, key := range []string{"daily_delete_at", "expire_time", "expire_unix"} {
		value, err := strconv.ParseInt(stack.Extra[key], 10, 64)
		if err != nil || value != deadline {
			return false
		}
	}
	return true
}
