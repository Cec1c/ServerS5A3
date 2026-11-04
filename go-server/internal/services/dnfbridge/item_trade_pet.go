package dnfbridge

import r "longheng.io/server/internal/modules/dnf/repository"

// Both the preview and authoritative exchange resolver apply the same rule.
// Pet bodies and food remain outside this artifact-only extension.
func currentPetArtifactTradeAllowed(def dungeonDropItemDefinition, stack r.ItemStack) bool {
	if !currentPVFItemTargetContainer(def).PetArtifact || stack.ItemID != int64(def.ItemID) || stack.Count != 1 || stack.Bind {
		return false
	}
	switch normalizeMagicBoxPVFType(def.AttachType) {
	case "free", "trade":
		return true
	case "sealing":
		return currentAuctionSealed(stack)
	}
	return false
}
