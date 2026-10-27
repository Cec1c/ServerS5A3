package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/accountnotice"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"strconv"
	"strings"
)

const currentAccountNoticeProgressOpcode uint16 = 503
const currentAccountNoticeProgressMsgID uint16 = 440

// The native second-group producer writes one u32, and never consumes an ACK.
func (s *Service) handleCurrentAccountNoticeProgress(session *gameSession, classification byte, body []byte) error {
	if s.options.currentClientVersion != "official-24.9" || session == nil || classification != proto.DefaultChannelClassification || len(body) != 4 || binary.LittleEndian.Uint32(body) >= accountnotice.Limit {
		return nil
	}
	accountID := strings.TrimSpace(session.accountID)
	if accountID == "" || session.selectedCharacterID == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !found || character.AccountID != accountID || character.Stats["delete_flag"] != 0 {
		return nil
	}
	if err := accountnotice.MarkViewed(ctx, repos.Account, accountID, body[0]); err != nil {
		s.logGameEvent(session, "game-account-notice-progress-save-failed", "notice_id", body[0], "error", err)
	}
	return nil
}

func (s *Service) sendCurrentAccountNoticeProgress(ctx context.Context, session *gameSession) error {
	if s.options.currentClientVersion != "official-24.9" || session == nil {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	ids, err := accountnotice.Load(ctx, repos.Account, s.accountIDForSession(session))
	if err != nil {
		s.logGameEvent(session, "game-account-notice-progress-load-failed", "error", err)
		return nil
	}
	// 440 replaces the whole second group. Empty accounts must clear the
	// preceding account's client state rather than inheriting its viewed flags.
	body := append([]byte{byte(len(ids))}, ids...)
	return s.sendGameUpperRawClassCodec(session, currentAccountNoticeProgressMsgID, body, 0, true)
}
