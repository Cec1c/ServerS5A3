package dnfbridge

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

// NOTI12/370 reject server groups >=250. Group zero is valid, including the
// legacy default when the character has no explicit server_group_id stat.
// Never derive this identity from a recipient, client hint, or channel ID.
func currentChatCharacterServerGroup(character dnfrepo.CharacterRecord) (byte, bool) {
	group := character.Stats["server_group_id"]
	if group < 0 || group >= 250 {
		return 0, false
	}
	return byte(group), true
}
