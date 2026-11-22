package dnfbridge

import "fmt"

// currentLocalActorMode0OwnerContext addresses only the selected player's
// native mode0 appearance descriptor. Dungeon runtime is committed before the
// pre-op27 binding stage, while enteredDungeon is not yet set. Do not reuse
// this value for mode1, peer rows, op9 or the room-slot table.
//
// 与支线（轻装上阵 local_actor_mode0_owner.go）逐字一致。
func currentLocalActorMode0OwnerContext(session *gameSession, existingOwner byte) (byte, error) {
	if !currentDungeonSceneActive(session) {
		return existingOwner, nil
	}
	owner := currentClientSelectedChannel(session)
	if owner == currentSceneObjectContext {
		return 0, fmt.Errorf("local dungeon mode0 owner is not representable: selected_channel=%d character_id=%d", currentClientSelectedChannelID(session), session.selectedCharacterID)
	}
	return owner, nil
}
