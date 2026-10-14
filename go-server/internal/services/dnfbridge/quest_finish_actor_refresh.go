package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

// 转职身份只取本次事务返回的角色；不重新查询 grow_type，也不复用旧版 USERINFO 包体。
func (s *Service) sendCurrentProfessionFinishActorIdentity(session *gameSession, result dnfquest.FinishCommitResult) error {
	if err := validateCurrentProfessionFinishResult(session, result); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if s.equipMode0RepositoryMemoEnabled() {
		ctx, _ = withEquipMode0RepositoryMemo(ctx)
	}
	character := result.PostCommitCharacter
	owner := currentSelectedActorAppearanceOwnerContext(session)
	body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx, session, session.selectedCharacterID, character.Name, character, true, owner,
	)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-upper-finish-profession-identity-send",
		"说明", "转职或觉醒已提交，刷新当前角色的职业身份",
		"quest_id", result.QuestID,
		"char_id", session.selectedCharacterID,
		"grow_before", result.Profession.PreviousGrowType,
		"grow_after", result.Profession.NewGrowType,
		"owner_context", owner,
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0,
		"body_len", len(body))
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
		return err
	}
	return s.sendBuffSwapReapply(session, "quest_profession_after_committed_identity")
}
