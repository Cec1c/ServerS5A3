package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/accountfriend"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

func (s *Service) handleCurrentAccountFriendCommand(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || op < 1436 || op > 1440 {
		return false, nil
	}
	if classification != 1 {
		return true, nil
	}
	r, err := decodeCurrentAccountFriendRequest(op, body)
	if err != nil {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return true, nil
	}
	if !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	repositories, available := s.repositoryGroup()
	if !available || repositories.Account == nil || repositories.Character == nil {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	source, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || source.AccountID != s.accountIDForSession(session) {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	owner := accountfriend.NewOwner(repositories)
	var target repo.AccountRecord
	if op == currentAccountFriendRemoveOpcode || op == currentAccountFriendNoteOpcode {
		entries, listErr := owner.List(ctx, source.AccountID)
		if listErr != nil {
			return true, s.sendGameUpperFailure(session, op, 3)
		}
		for _, entry := range entries {
			if entry.Relation.State != accountfriend.Friend {
				continue
			}
			characters, listErr := repositories.Character.ListByAccount(ctx, entry.Account.AccountID, repo.DefaultCharacterSlotLimit)
			if listErr != nil {
				return true, s.sendGameUpperFailure(session, op, 3)
			}
			for _, c := range characters {
				if c.Name == r.Target && currentAdventureCharacterServerGroup(c) == currentAdventureCharacterServerGroup(source) {
					target = entry.Account
					break
				}
			}
			if target.AccountID != "" {
				break
			}
		}
	} else {
		id, exists, findErr := findRepresentAccountName(ctx, repositories.Account, r.Target)
		if findErr != nil {
			return true, s.sendGameUpperFailure(session, op, 3)
		}
		if exists {
			target, found, err = repositories.Account.Load(ctx, id)
			if err != nil || !found || target.RepresentAccountName != r.Target {
				return true, s.sendGameUpperFailure(session, op, 21)
			}
		}
	}
	if target.AccountID == "" {
		return true, s.sendGameUpperFailure(session, op, 21)
	}
	if target.AccountID == source.AccountID {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return true, s.sendGameUpperFailure(session, op, 3)
	}
	cmd := accountfriend.Command{AccountID: source.AccountID, CharacterID: source.CharacterID, TargetAccountID: target.AccountID, TargetName: target.RepresentAccountName, Text: r.Text}
	switch op {
	case 1436:
		err = owner.Request(ctx, cmd)
	case 1437:
		err = owner.Accept(ctx, cmd)
	case 1438:
		err = owner.Delete(ctx, cmd)
	case 1439:
		err = owner.RefuseCancel(ctx, cmd, r.Action)
	case 1440:
		err = owner.UpdateNote(ctx, cmd)
	}
	if err != nil {
		code := byte(3)
		if errors.Is(err, accountfriend.ErrMissing) {
			code = 21
		} else if errors.Is(err, accountfriend.ErrInvitation) && op != 1436 {
			code = 19
		} else if errors.Is(err, accountfriend.ErrAlreadyFriend) {
			code = 18
		} else if errors.Is(err, accountfriend.ErrAlready) {
			code = 20
		}
		s.logGameEvent(session, "game-account-friend-rejected", "opcode", op, "reason", "domain_transaction", "error", err)
		return true, s.sendGameUpperFailure(session, op, code)
	}
	tail, err := buildCurrentAccountFriendSuccessTail(op, r)
	if err != nil {
		return true, err
	}
	if err = s.sendGameUpperSuccess(session, op, tail); err != nil {
		return true, err
	}
	s.logGameEvent(session, "game-account-friend-persisted", "opcode", op, "source_char_id", source.CharacterID)
	if err = s.sendCurrentAccountFriendLists(session, "mutation"); err != nil {
		return true, err
	}
	s.refreshCurrentAccountFriendOwners(session, []string{source.AccountID, target.AccountID}, "mutation")
	return true, nil
}

func (s *Service) currentAccountFriendProjection(ctx context.Context, accountID string) ([]currentAccountFriendNativeRow, []currentAccountFriendPendingRow, error) {
	repositories, available := s.repositoryGroup()
	if !available || repositories.Account == nil || repositories.Character == nil {
		return nil, nil, accountfriend.ErrUnavailable
	}
	entries, err := accountfriend.NewOwner(repositories).List(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	var rows []currentAccountFriendNativeRow
	var pending []currentAccountFriendPendingRow
	for _, entry := range entries {
		if entry.Relation.State != accountfriend.Friend {
			// NOTI1372's kind0 reaches manager+0x28 (received requests),
			// while kind1 reaches +0x14 (sent requests). Verified against
			// current v95's 02008910 and the two owned clients' directory UI.
			kind := byte(1)
			if entry.Relation.State == accountfriend.Incoming {
				kind = 0
			}
			expiry := entry.Relation.ExpiresAt.Unix()
			if expiry <= 0 || expiry > 0xffffffff {
				return nil, nil, errCurrentSocialBodyInvalid
			}
			pending = append(pending, currentAccountFriendPendingRow{Kind: kind, AdventureName: entry.Account.RepresentAccountName, Message: entry.Relation.Message, ExpiresUnix: uint32(expiry)})
			continue
		}
		characters, err := repositories.Character.ListByAccount(ctx, entry.Account.AccountID, repo.DefaultCharacterSlotLimit)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range characters {
			if c.AccountID != entry.Account.AccountID {
				return nil, nil, errCurrentSocialBodyInvalid
			}
			id, err := strconv.ParseUint(c.CharacterID, 10, 16)
			job := numericCharacterStat(c.Job)
			if err != nil || id == 0 || c.Level < 0 || c.Level > 255 || job < 0 || job > 255 {
				return nil, nil, errCurrentSocialBodyInvalid
			}
			channel := uint16(0)
			if online, ok := s.onlineGameSessionCharacterSnapshot(uint16(id)); ok && s.accountIDForSession(online.session) == c.AccountID && s.boundGameSessionCharacterCurrent(online) {
				// NOTI1371 carries a u16 real channel, unlike actor owner
				// fields. Do not substitute the resident u8 channel here.
				if ch := online.session.channel.ID; ch > 0 && ch <= 0xffff {
					channel = uint16(ch)
				}
			}
			rows = append(rows, currentAccountFriendNativeRow{Head: uint16(currentAdventureCharacterServerGroup(c)), AdventureName: entry.Account.RepresentAccountName, Level: byte(c.Level), Job: byte(job), Grow: characterGrowTypeByte(c), Channel: channel, CharacterName: c.Name, Remark: entry.Relation.Note})
		}
	}
	return rows, pending, nil
}

func (s *Service) sendCurrentAccountFriendLists(session *gameSession, source string) error {
	if s.options.currentClientVersion != "official-24.9" || session == nil || !session.loginPacketSeen || !session.accountFromToken {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil {
		return nil
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || character.AccountID != s.accountIDForSession(session) {
		s.logGameEvent(session, "game-account-friend-list-deferred", "source", source, "reason", "selected_owner_unverified", "error", err)
		return nil
	}
	rows, pending, err := s.currentAccountFriendProjection(ctx, s.accountIDForSession(session))
	if err != nil {
		s.logGameEvent(session, "game-account-friend-list-deferred", "source", source, "error", err)
		return nil
	}
	pendingBody, err := buildCurrentAccountFriendPendingBody(pending)
	if err != nil {
		return err
	}
	// 1371 upserts by character name; 1373 removes vanished/renamed/deleted rows.
	// The cache is session presentation state, never authoritative friendships.
	names := make(map[string]struct{}, len(rows))
	var bodies [][]byte
	for _, row := range rows {
		names[row.CharacterName] = struct{}{}
	}
	if len(rows) == 0 {
		body, _ := buildCurrentAccountFriendInfoBody(nil)
		bodies = append(bodies, body)
	}
	for start := 0; start < len(rows); start += 255 {
		end := start + 255
		if end > len(rows) {
			end = len(rows)
		}
		body, err := buildCurrentAccountFriendInfoBody(rows[start:end])
		if err != nil {
			return err
		}
		bodies = append(bodies, body)
	}
	var removals [][]byte
	for name := range session.currentAccountFriendNames {
		if _, exists := names[name]; !exists {
			body, err := buildCurrentAccountFriendDeleteBody(name)
			if err != nil {
				return err
			}
			removals = append(removals, body)
		}
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	for _, body := range removals {
		if err := s.sendGameUpperRawClassCodec(session, 1373, body, 0, true); err != nil {
			return err
		}
	}
	for _, body := range bodies {
		if err := s.sendGameUpperRawClassCodec(session, 1371, body, 0, true); err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClassCodec(session, 1372, pendingBody, 0, true); err != nil {
		return err
	}
	session.currentAccountFriendNames = names
	s.logGameEvent(session, "game-account-friend-list-send", "source", source, "characters", len(rows), "pending", len(pending), "removed", len(removals))
	return nil
}

func (s *Service) refreshCurrentAccountFriendOwners(exclude *gameSession, owners []string, source string) {
	allowed := make(map[string]struct{}, len(owners))
	for _, id := range owners {
		allowed[id] = struct{}{}
	}
	var peers []boundGameSessionCharacter
	s.mu.Lock()
	for id, session := range s.gameSessions {
		if session == nil || session == exclude {
			continue
		}
		if _, ok := allowed[s.accountIDForSession(session)]; ok {
			peers = append(peers, boundGameSessionCharacter{session: session, character: id, generation: session.characterGeneration})
		}
	}
	s.mu.Unlock()
	for _, peer := range peers {
		peer := peer
		_ = s.postGameSessionCharacterEvent(peer.session, "account_friend_"+source, peer.character, peer.generation, func() error {
			if !s.boundGameSessionCharacterCurrent(peer) {
				return nil
			}
			return s.sendCurrentAccountFriendLists(peer.session, source)
		})
	}
}

func (s *Service) refreshCurrentAccountFriendPresence(session *gameSession, source string) {
	if s.options.currentClientVersion != "official-24.9" || session == nil || !session.loginPacketSeen || !session.accountFromToken {
		return
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	entries, err := accountfriend.NewOwner(repositories).List(ctx, s.accountIDForSession(session))
	if err != nil {
		return
	}
	var owners []string
	for _, entry := range entries {
		if entry.Relation.State == accountfriend.Friend {
			owners = append(owners, entry.Account.AccountID)
		}
	}
	s.refreshCurrentAccountFriendOwners(session, owners, source)
}
