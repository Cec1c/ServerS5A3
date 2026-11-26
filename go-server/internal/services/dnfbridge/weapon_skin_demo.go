package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

const (
	weaponSkinDemoOpcode     uint16 = 0x7f04
	weaponSkinDemoAvatarSlot        = 10
	// 现用 90 读 mode0 每项的辅助 DWORD 到临时变量后丢弃。
	// WSV1 只占仓库自己的槽 10 投影，用来区别同一件装扮被真实穿戴。
	weaponSkinDemoVisualMarker int64 = 0x31565357
)

type weaponSkinDemoRequest struct {
	requestID  uint32
	epoch      uint32
	action     weaponskin.Action
	revision   uint32
	appearance uint32
	catalog    bool
}

func decodeWeaponSkinDemoRequest(body []byte) (weaponSkinDemoRequest, bool) {
	legacy := len(body) == 20 && string(body[:4]) == "WSQ1"
	catalog := len(body) == 28 && string(body[:4]) == "WSQ2" && binary.LittleEndian.Uint32(body[24:28]) == weaponskin.CatalogChecksum
	if (!legacy && !catalog) || body[13] != 0 || body[14] != 0 || body[15] != 0 {
		return weaponSkinDemoRequest{}, false
	}
	request := weaponSkinDemoRequest{
		requestID: binary.LittleEndian.Uint32(body[4:8]),
		epoch:     binary.LittleEndian.Uint32(body[8:12]),
		action:    weaponskin.Action(body[12]),
		revision:  binary.LittleEndian.Uint32(body[16:20]),
	}
	if catalog {
		request.catalog = true
		request.appearance = binary.LittleEndian.Uint32(body[20:24])
		if (request.action == weaponskin.Apply) != (request.appearance != 0) {
			return request, false
		}
	}
	return request, request.requestID != 0 && request.epoch != 0 && request.action <= weaponskin.Clear &&
		(request.action != weaponskin.List || request.revision == 0)
}

func weaponSkinDemoGameplayModule() gameplayModuleDefinition {
	handler := func(s *Service, session *gameSession, request gameplayRequest) error {
		if request.Classification != 1 {
			return nil
		}
		return s.handleWeaponSkinDemo(session, request.Body)
	}
	return gameplayModuleDefinition{
		Name:           "weapon-skin-demo",
		LegacyHandlers: map[uint16]gameplayHandler{weaponSkinDemoOpcode: handler},
		UpperHandlers:  map[uint16]gameplayHandler{weaponSkinDemoOpcode: handler},
	}
}

func (s *Service) handleWeaponSkinDemo(session *gameSession, body []byte) error {
	if len(body) >= 4 {
		switch string(body[:4]) {
		case "WSF1", "WSF2":
			return s.handleWeaponSkinForge(session, body)
		case "WSO1":
			return s.handleSkinWarehouseSummary(session, body)
		}
	}
	request, ok := decodeWeaponSkinDemoRequest(body)
	if !ok || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	now := s.gameplayNow()
	if now.Sub(session.weaponSkinDemoLastRequest) < 250*time.Millisecond {
		return s.sendWeaponSkinDemo(session, request, 4, weaponskin.State{})
	}
	session.weaponSkinDemoLastRequest = now
	current := func() bool {
		return !session.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(bound)
	}
	if session.townSceneReadyCharacterID != bound.character || session.preDungeonContextPlayerStateSent {
		return s.sendWeaponSkinDemo(session, request, 1, weaponskin.State{})
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || !weaponSkinDemoAssetReady(catalog) {
		return s.sendWeaponSkinDemo(session, request, 1, weaponskin.State{})
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.CharacterAssets == nil {
		return s.sendWeaponSkinDemo(session, request, 1, weaponskin.State{})
	}
	command := weaponskin.Command{
		CharacterID: identity.CharacterID, AccountID: identity.AccountID,
		Action: request.action, Revision: request.revision, Now: now, Current: current, Appearance: request.appearance,
	}
	if request.catalog {
		command.Available = func(a weaponskin.Appearance) bool { return weaponSkinAssetReady(catalog, a) }
	}
	state, err := weaponskin.Update(ctx, repos.CharacterAssets, command)
	if err == nil && request.action != weaponskin.List {
		s.invalidateTownMode0Character(bound.character)
	}
	if !current() {
		return nil
	}
	status := byte(0)
	switch {
	case errors.Is(err, weaponskin.ErrNotOwned):
		status = 2
	case errors.Is(err, weaponskin.ErrRevision):
		status = 3
	case err != nil:
		status = 1
		state = weaponskin.State{}
	}
	if err := s.sendWeaponSkinDemo(session, request, status, state); err != nil {
		return err
	}
	if status == 0 && request.action != weaponskin.List {
		if _, err := s.sendSelectedActorMode0AppearanceRefreshGuarded(session, "weapon_skin_demo", "保存武器外观后同步角色", current); err != nil {
			s.logGameEvent(session, "武器外观即时刷新失败", "char_id", bound.character)
		}
	}
	return nil
}

func (s *Service) sendWeaponSkinDemo(session *gameSession, request weaponSkinDemoRequest, status byte, state weaponskin.State) error {
	if request.catalog {
		body := make([]byte, 28+(len(weaponskin.Catalog())+7)/8)
		copy(body[:4], "WSR2")
		binary.LittleEndian.PutUint32(body[4:8], request.requestID)
		binary.LittleEndian.PutUint32(body[8:12], request.epoch)
		body[12], body[13] = status, byte(request.action)
		binary.LittleEndian.PutUint32(body[16:20], weaponskin.CatalogChecksum)
		binary.LittleEndian.PutUint32(body[20:24], state.Selected)
		binary.LittleEndian.PutUint32(body[24:28], state.Revision)
		copy(body[28:], state.Unlocked)
		return s.sendGameUpperFixed16Transport(session, weaponSkinDemoOpcode, body, 1, 0, false, "武器外观目录回执")
	}
	var body [28]byte
	copy(body[:4], "WSR1")
	binary.LittleEndian.PutUint32(body[4:8], request.requestID)
	binary.LittleEndian.PutUint32(body[8:12], request.epoch)
	body[12] = status
	body[13] = byte(request.action)
	if state.Owned {
		body[14] = 1
		binary.LittleEndian.PutUint32(body[16:20], weaponskin.SampleID)
	}
	binary.LittleEndian.PutUint32(body[20:24], state.Selected)
	binary.LittleEndian.PutUint32(body[24:28], state.Revision)
	return s.sendGameUpperFixed16Transport(session, weaponSkinDemoOpcode, body[:], 1, 0, false, "weapon_skin_demo_fixed16_plain")
}

func weaponSkinDemoAssetReady(catalog *pvfDungeonDropCatalog) bool {
	a, _ := weaponskin.FromAppearance(weaponskin.SampleID)
	return weaponSkinAssetReady(catalog, a)
}

func weaponSkinAssetReady(catalog *pvfDungeonDropCatalog, a weaponskin.Appearance) bool {
	item, err := catalog.ResolveItem(a.ID)
	return err == nil && item.Kind == dungeonDropItemEquipment &&
		item.EquipmentType == "[weapon avatar]" && item.PVFPath == a.Path
}

func (s *Service) weaponSkinDemoRosterAppearance(character dnfrepo.CharacterRecord) []dnfrepo.CharacterRosterEquipSummary {
	rows := character.Roster.Entry.EquipSummary
	selected := weaponskin.Selected(character)
	a, found := weaponskin.FromAppearance(selected)
	if !found {
		return rows
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || !weaponSkinAssetReady(catalog, a) {
		return rows
	}
	projected := make([]dnfrepo.CharacterRosterEquipSummary, 0, len(rows)+1)
	for _, row := range rows {
		if row.Slot != weaponSkinDemoAvatarSlot {
			projected = append(projected, row)
		}
	}
	projected = append(projected, dnfrepo.CharacterRosterEquipSummary{Slot: weaponSkinDemoAvatarSlot, ItemIDOrIcon: int64(selected)})
	return filterRosterWeaponAppearance(projected)
}

func (s *Service) applyWeaponSkinDemoSceneAppearance(rows []dnfrepo.CharacterRosterEquipSummary, character dnfrepo.CharacterRecord) {
	if s.options.currentClientVersion != "official-24.9" || len(rows) < currentActorMode0AppearanceSlotCount {
		return
	}
	avatar := &rows[weaponSkinDemoAvatarSlot]
	if avatar.Slot != weaponSkinDemoAvatarSlot {
		return
	}
	selected := weaponskin.Selected(character)
	a, found := weaponskin.FromAppearance(selected)
	if !found {
		return
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || !weaponSkinAssetReady(catalog, a) {
		return
	}
	// 仓库选择优先显示。实穿装扮的链接及其他外观元数据不属于样板。
	*avatar = dnfrepo.CharacterRosterEquipSummary{
		Slot: weaponSkinDemoAvatarSlot, ItemIDOrIcon: int64(selected), AuxValue: weaponSkinDemoVisualMarker,
	}
}

// 副本首房的完整 mode1 会重建真正的穿戴栏。等读条落位后，再用同一个
// owner 的 mode0 恢复仓库外观。穿戴栏及其下发顺序沿用原规则。
func (s *Service) restoreDungeonWeaponSkinDemoAppearance(ctx context.Context, session *gameSession, charID uint16, charName string, character dnfrepo.CharacterRecord, source string) error {
	if !session.postStartMapPlayerStateSent || s.options.currentClientVersion != "official-24.9" || weaponskin.Selected(character) == 0 {
		return nil
	}
	body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, session, charID, charName, character, true, currentSceneObjectContext)
	if err != nil {
		return err
	}
	preserveActiveDungeonSceneObjectUserState(session, charID, currentSceneObjectContext, body)
	return s.sendWeaponSkinDemoAppearanceAfterEquipment(session, charID, character, body, source)
}

func (s *Service) weaponSkinAppearanceAfterEquipment(character dnfrepo.CharacterRecord, body []byte) uint32 {
	selected := weaponskin.Selected(character)
	if s.options.currentClientVersion != "official-24.9" || selected == 0 || !currentWeaponSkinBodyHasAppearance(body, selected) {
		return 0
	}
	return selected
}

func (s *Service) sendWeaponSkinDemoAppearanceAfterEquipment(receiver *gameSession, charID uint16, character dnfrepo.CharacterRecord, body []byte, source string) error {
	selected := s.weaponSkinAppearanceAfterEquipment(character, body)
	if selected == 0 {
		return nil
	}
	return s.withGameWire(receiver, func() error {
		return s.writeWeaponSkinAppearanceAfterEquipment(receiver, charID, body, source, selected)
	})
}

// Call with the receiver's wire lock held.
func (s *Service) writeWeaponSkinAppearanceAfterEquipment(receiver *gameSession, charID uint16, body []byte, source string, selected uint32) error {
	if err := s.sendGameUpperRawClassCodecLocked(receiver, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0, true); err != nil {
		return err
	}
	s.logPacketEvent("武器外观穿戴重建后补发", "conn_id", receiver.connID, "source", source,
		"char_id", charID, "owner_channel", body[4], "item_id", selected)
	return nil
}

func currentWeaponSkinDemoBodyHasSample(body []byte) bool {
	return currentWeaponSkinBodyHasAppearance(body, weaponskin.SampleID)
}

func currentWeaponSkinBodyHasAppearance(body []byte, id uint32) bool {
	if len(body) < 5 || body[0] != 0 {
		return false
	}
	_, tail, ok := currentSceneObjectLevelForLog(body)
	if !ok || tail+6 >= len(body) {
		return false
	}
	// mode0 原生写入器给每一行写空扩展块。读实际槽号和显示 ID，
	// 仅在这一帧实际包含有效样板时补发。
	pos := tail + 7
	for i := 0; i < int(body[tail+6]); i++ {
		if pos+currentSceneObjectEquipSummaryRowBytes > len(body) || binary.LittleEndian.Uint32(body[pos+5:pos+9]) != 0 {
			return false
		}
		if body[pos] == weaponSkinDemoAvatarSlot {
			return binary.LittleEndian.Uint32(body[pos+1:pos+5]) == id &&
				binary.LittleEndian.Uint32(body[pos+14:pos+18]) == uint32(weaponSkinDemoVisualMarker)
		}
		pos += currentSceneObjectEquipSummaryRowBytes
	}
	return false
}
