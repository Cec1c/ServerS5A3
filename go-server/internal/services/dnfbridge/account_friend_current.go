package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/accountfriend"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"slices"
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
		// The adventure-group friend list is a presence projection: it must list
		// only the friend characters that are actually online. Enumerating the
		// whole account roster (including offline characters with channel 0) is
		// what the current client reported as "shows every character of the
		// friend's adventure group at once".
		// One friend account (adventure group) is exactly one top-level row.
		// Characters of the same account must never become independent top-level
		// rows, so at most one online character is projected and the durable
		// last-selected character is used when none is online.
		var onlineChars []repo.CharacterRecord
		var onlineChannels []uint16
		for _, c := range characters {
			if c.AccountID != entry.Account.AccountID {
				return nil, nil, errCurrentSocialBodyInvalid
			}
			channel, live := s.currentAccountFriendOnlineChannel(c)
			if !live {
				continue
			}
			onlineChars = append(onlineChars, c)
			onlineChannels = append(onlineChannels, channel)
		}
		if len(onlineChars) > 0 {
			idx := selectAccountFriendRepresentative(entry, onlineChars)
			row, err := currentAccountFriendRowForCharacter(entry, onlineChars[idx], onlineChannels[idx])
			if err != nil {
				return nil, nil, err
			}
			rows = append(rows, row)
			continue
		}
		// The friend has no live character. Fall back to the durable character the
		// account last selected (written on successful selection), then to the peer
		// character recorded on the accepted relation for friendships that predate
		// that record. Never the offline roster, never an arbitrary "first"
		// character; without any durable identity the row is omitted.
		last, ok := s.currentAccountFriendOfflineCharacter(ctx, repositories, entry)
		if !ok {
			continue
		}
		row, err := currentAccountFriendRowForCharacter(entry, last, 0)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
	}
	return rows, pending, nil
}

// accountFriendCharacterIDNumber reports the decimal value of a character id for
// ORDERING ONLY. It is deliberately not a validity gate: the projection already
// decides which characters may be projected through its own u16 gates
// (currentAccountFriendOnlineChannel and currentAccountFriendRowForCharacter),
// and the representative choice must never turn a character that production
// would merely skip into a whole-page failure. 64-bit decimal keeps every
// numeric spelling (including ids above 65535) in the same numeric class.
func accountFriendCharacterIDNumber(id string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// compareAccountFriendCharacterID is a strict total order over character ids.
// It is a single ordered key, never "numeric when both parse, string otherwise",
// because that rule is not transitive (9 < 10 < "1a" < 9) and would make the
// representative depend on the enumeration order:
//
//	class 0: valid decimal ids, ordered by numeric value, then by the trimmed
//	         spelling so equal values with different spellings ("01" vs "1")
//	         still have a defined tie-break;
//	class 1: everything else, ordered by trimmed spelling;
//	class 0 always sorts before class 1.
//
// This is an ordering rule only. It rejects nothing and the projection does not
// reject class 1 either: the candidate set is exactly the characters the
// projection already admitted, so a non-decimal spelling can never fail the
// list - it only sorts after every decimal id. The class rule exists to make the
// order total (and therefore permutation invariant) for every input.
func compareAccountFriendCharacterID(a, b string) int {
	av, aok := accountFriendCharacterIDNumber(a)
	bv, bok := accountFriendCharacterIDNumber(b)
	at, bt := strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case aok && bok:
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	case aok != bok:
		if aok {
			return -1
		}
		return 1
	}
	if at == bt {
		return 0
	}
	if at < bt {
		return -1
	}
	return 1
}

// selectAccountFriendRepresentative picks the single character projected for a
// friend adventure group while at least one of its characters is online.
// Precedence:
//
//  1. the account's durable last-selected character, when that character is one
//     of the online ones: the row then does not churn as sibling characters log
//     in and out of the same adventure group;
//  2. otherwise the least online character id under the total order above:
//     deterministic, so any permutation of the same online set yields the same
//     row.
//
// It never rejects and never returns an error: the candidate set is exactly the
// one the projection already admitted, so the helper cannot widen the projection
// error surface. The returned index addresses online (-1 for an empty set).
func selectAccountFriendRepresentative(entry accountfriend.Entry, online []repo.CharacterRecord) int {
	if len(online) == 0 {
		return -1
	}
	if want := strings.TrimSpace(entry.Account.Metadata[lastOnlineCharacterMetadataKey]); want != "" {
		for i, c := range online {
			if strings.TrimSpace(c.CharacterID) == want {
				return i
			}
		}
	}
	best := 0
	for i := 1; i < len(online); i++ {
		if compareAccountFriendCharacterID(online[i].CharacterID, online[best].CharacterID) < 0 {
			best = i
		}
	}
	return best
}

// currentAccountFriendOnlineChannel resolves the live directory owner for one
// character. A session counts only when it owns the character directory entry,
// belongs to the character account, is fully logged in, and still holds the
// same generation. The row keeps the real u16 channel from that session;
// NOTI1371 carries a u16 real channel, unlike actor owner fields, so the
// resident u8 channel is never substituted.
func (s *Service) currentAccountFriendOnlineChannel(c repo.CharacterRecord) (uint16, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.CharacterID), 10, 16)
	if err != nil || id == 0 {
		return 0, false
	}
	online, ok := s.onlineGameSessionCharacterSnapshot(uint16(id))
	if !ok || online.session == nil {
		return 0, false
	}
	if !online.session.loginPacketSeen || !online.session.accountFromToken || s.accountIDForSession(online.session) != c.AccountID || !s.boundGameSessionCharacterCurrent(online) {
		return 0, false
	}
	if ch := online.session.channel.ID; ch > 0 && ch <= 0xffff {
		return uint16(ch), true
	}
	return 0, true
}

// currentAccountFriendOfflineCharacter resolves the single character to show for
// an offline friend. Precedence:
//
//  1. the account's durable last-selected character
//     (lastOnlineCharacterMetadataKey, written on successful selection);
//  2. the peer character durably recorded on the accepted relation, for
//     friendships created before that record existed.
//
// Both are durable, account-scoped character identities; the row is omitted when
// neither resolves. The repository keeps no separate last-login clock.
func (s *Service) currentAccountFriendOfflineCharacter(ctx context.Context, repositories repo.Group, entry accountfriend.Entry) (repo.CharacterRecord, bool) {
	if repositories.Character == nil {
		return repo.CharacterRecord{}, false
	}
	candidates := [2]string{
		strings.TrimSpace(entry.Account.Metadata[lastOnlineCharacterMetadataKey]),
		strings.TrimSpace(entry.Relation.PeerCharacterID),
	}
	for _, id := range candidates {
		if id == "" {
			continue
		}
		character, found, err := repositories.Character.Load(ctx, id)
		if err != nil || !found || character.CharacterID != id || strings.TrimSpace(character.AccountID) != strings.TrimSpace(entry.Account.AccountID) {
			continue
		}
		return character, true
	}
	return repo.CharacterRecord{}, false
}

func currentAccountFriendRowForCharacter(entry accountfriend.Entry, c repo.CharacterRecord, channel uint16) (currentAccountFriendNativeRow, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.CharacterID), 10, 16)
	job := numericCharacterStat(c.Job)
	if err != nil || id == 0 || c.Level < 0 || c.Level > 255 || job < 0 || job > 255 {
		return currentAccountFriendNativeRow{}, errCurrentSocialBodyInvalid
	}
	return currentAccountFriendNativeRow{Head: uint16(currentAdventureCharacterServerGroup(c)), AdventureName: entry.Account.RepresentAccountName, Level: byte(c.Level), Job: byte(job), Grow: characterGrowTypeByte(c), Channel: channel, CharacterName: c.Name, Remark: entry.Relation.Note}, nil
}

// NOTI1371 is an upsert, not a silent snapshot replacement. The current EXE
// (0201FE50, notification branch 02020098) announces a single online row whenever
// its friend manager is already nonempty. Replaying an unchanged row at every
// room's finish-loading therefore repeats the online toast. Keep the successful
// projection for this selected character generation; reconnects and selections
// must bootstrap their own native manager again.
type currentAccountFriendSentView struct {
	character  uint16
	generation uint64
	rows       []currentAccountFriendNativeRow
	ready      bool
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
	previous := session.currentAccountFriendInfo
	unchanged := previous.ready && previous.character == identity.character &&
		previous.generation == identity.generation && slices.Equal(previous.rows, rows)
	if !unchanged {
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
		// A later pending-list failure must not replay already delivered online
		// rows. Failed info/removal writes leave the last successful view intact.
		session.currentAccountFriendNames = names
		session.currentAccountFriendInfo = currentAccountFriendSentView{
			character: identity.character, generation: identity.generation,
			rows: slices.Clone(rows), ready: true,
		}
	}
	if err := s.sendGameUpperRawClassCodec(session, 1372, pendingBody, 0, true); err != nil {
		return err
	}
	s.logGameEvent(session, "game-account-friend-list-send", "source", source, "characters", len(rows), "pending", len(pending), "removed", len(removals), "info_unchanged", unchanged)
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
