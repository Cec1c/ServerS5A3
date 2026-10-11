package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"sort"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentAdventureRepresentRequestLength = 2
	currentAdventureRepresentGrowMask      = byte(0x0f)
	currentAdventureRepresentTypeMask      = byte(0x70)
	currentAdventureRepresentType          = byte(0x20)
	currentAdventureRepresentEntrySize     = 52
	currentAdventureRepresentGroupOffset   = 0
	currentAdventureRepresentStyleOffset   = 4
	currentAdventureRepresentLevelOffset   = 8
	currentAdventureRepresentIDOffset      = 12
	currentAdventureRepresentNameOffset    = 20
	currentAdventureRepresentNameSize      = 30
)

// handleCurrentAdventureRepresentCharacters follows current NoPack
// E5F500/E62060. The request is u8 job plus the packed grow byte
// produced by 2961490(grow, 2). The success body groups matching account
// characters by server-group byte. Its 52-byte row is specific to op1473 and
// must not reuse the differently laid out op1339/op1401 roster record.
func (s *Service) handleCurrentAdventureRepresentCharacters(
	session *gameSession,
	request []byte,
) error {
	opcode := currentAdventureRepresentCharactersMsgID
	if len(request) != currentAdventureRepresentRequestLength ||
		request[1]&currentAdventureRepresentTypeMask != currentAdventureRepresentType {
		return s.sendGameUpperFailure(session, opcode, 3)
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		s.logGameEvent(session, "game-adventure-represent-characters-rejected",
			"job", request[0],
			"grow", request[1]&currentAdventureRepresentGrowMask,
			"error", err)
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}

	job := request[0]
	grow := request[1] & currentAdventureRepresentGrowMask
	groupID := s.channelServerID()
	if groupID <= 0 || groupID > 255 {
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	body := buildCurrentAdventureRepresentCharactersBodyForServer(job, grow, state.Characters, byte(groupID))
	s.logGameEvent(session, "game-adventure-represent-characters-send",
		"job", job,
		"grow", grow,
		"roster_count", countCurrentAdventureRepresentCharacters(job, grow, state.Characters),
		"body_len", len(body))
	return s.sendGameUpperSuccess(session, opcode, body)
}

func buildCurrentAdventureRepresentCharactersBody(
	job byte,
	grow byte,
	characters []dnfrepo.CharacterRecord,
) []byte {
	return buildCurrentAdventureRepresentCharactersBodyForServer(job, grow, characters, 1)
}

func buildCurrentAdventureRepresentCharactersBodyForServer(job, grow byte, characters []dnfrepo.CharacterRecord, sourceServerGroup byte) []byte {
	grouped := make(map[byte][]dnfrepo.CharacterRecord)
	for _, character := range characters {
		if byte(numericCharacterStat(character.Job)) != job ||
			characterGrowTypeByte(character)&currentAdventureRepresentGrowMask != grow ||
			!currentAdventureCollectBookEligible(character) {
			continue
		}
		group, validGroup := adventuregroup.CollectionServerGroup(character, sourceServerGroup)
		if !validGroup {
			continue
		}
		grouped[group] = append(grouped[group], character)
	}

	groups := make([]int, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, int(group))
	}
	sort.Ints(groups)
	if len(groups) > 255 {
		groups = groups[:255]
	}

	var writer packetWriter
	writer.writeByte(byte(len(groups)))
	for _, groupValue := range groups {
		group := byte(groupValue)
		rows := grouped[group]
		sort.SliceStable(rows, func(left, right int) bool {
			if rows[left].Slot != rows[right].Slot {
				return rows[left].Slot < rows[right].Slot
			}
			return numericCharacterID(rows[left]) < numericCharacterID(rows[right])
		})
		if len(rows) > 255 {
			rows = rows[:255]
		}
		writer.writeByte(group)
		writer.writeByte(byte(len(rows)))
		for _, character := range rows {
			entry := make([]byte, currentAdventureRepresentEntrySize)
			writeCurrentAdventureRepresentEntry(entry, group, character)
			writer.writeBytes(entry)
		}
	}
	return writer.bytes()
}

func writeCurrentAdventureRepresentEntry(
	entry []byte,
	group byte,
	character dnfrepo.CharacterRecord,
) {
	if len(entry) < currentAdventureRepresentEntrySize {
		return
	}
	entry = entry[:currentAdventureRepresentEntrySize]
	clear(entry)

	// Current NoPack sub_9F2810/sub_9F3F70 read the row directly:
	//   +0  u8  server group used by each list card
	//   +4  u32 honor level; BEB460 also matches this against the saved card
	//   +8  u32 character level rendered by the "Lv%d" label
	//   +12 u32 character ID sent back by op1474
	//   +20 char[30] GB18030 character name
	entry[currentAdventureRepresentGroupOffset] = group
	binary.LittleEndian.PutUint32(
		entry[currentAdventureRepresentStyleOffset:currentAdventureRepresentStyleOffset+4],
		currentAdventureInfoRosterHonorLevel(character),
	)
	binary.LittleEndian.PutUint32(
		entry[currentAdventureRepresentLevelOffset:currentAdventureRepresentLevelOffset+4],
		uint32(rosterLevel(character)),
	)
	binary.LittleEndian.PutUint32(
		entry[currentAdventureRepresentIDOffset:currentAdventureRepresentIDOffset+4],
		uint32(numericCharacterID(character)),
	)
	name := currentAdventureInfoRosterNameBytes(character.Name)
	copy(
		entry[currentAdventureRepresentNameOffset:currentAdventureRepresentNameOffset+currentAdventureRepresentNameSize],
		name,
	)
}

func countCurrentAdventureRepresentCharacters(
	job byte,
	grow byte,
	characters []dnfrepo.CharacterRecord,
) int {
	count := 0
	for _, character := range characters {
		if byte(numericCharacterStat(character.Job)) == job &&
			characterGrowTypeByte(character)&currentAdventureRepresentGrowMask == grow &&
			currentAdventureCollectBookEligible(character) {
			count++
		}
	}
	return count
}

// currentAdventureCollectBookEligible reports whether one account character may
// appear in the adventure-group 角色收集簿 (character collection book) that
// op1473 fills.
//
// The runtime PVF states the collection condition twice:
//   - etc/etc.chn.str key `adv_collection_tooltip00`:
//     "可以设置各个转职的代表角色。- 条件 : Lv90级以上且二次觉醒的角色。"
//     plus popup_menu_200 "设置代表角色" — i.e. this is exactly the per-job
//     representative-character book op1473/op1474 serve.
//   - etc/adventurersystem/adventurersystem2018.etc `[expert list]` > `[group]`
//     > `[info]` rows carry one `[collection movie]` per job group, so the book
//     only ever holds characters that met that level condition.
//
// The threshold is the playable cap of the current EXE/profile compatibility
// unit, not a guessed number (the progression PVF keeps later rows for
// compatibility, so its row count is not a playable cap).
//
// A Lv40 character still legitimately belongs to the 冒险团 itself — the PVF
// `[point bonus]` table in etc/linksystem/charactermanage.etc starts at level 40
// and guide_text_138 says the adventure group uses Lv40+ characters — so the
// level gate is applied only to the collection book, never to the roster that
// feeds the account summary.
func currentAdventureCollectBookEligible(character dnfrepo.CharacterRecord) bool {
	_, eligible := adventuregroup.CollectionKey(character, currentAdventureCharacterLevelCap)
	return eligible
}

// E5E9A0 writes server-group8, character32, job8, packed-grow8. E63C40
// reads a serialized 3-byte group counter and serialized 52-byte roster row.
func (s *Service) handleCurrentAdventureSelectRepresentative(session *gameSession, request []byte) error {
	opcode := currentAdventureSelectRepresentativeMsgID
	if len(request) != 7 || request[6]&0xf0 != currentAdventureRepresentType || request[6]&0x0f == 0 {
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	job, branch := request[5], request[6]&0x0f
	sourceGroup := s.channelServerID()
	if sourceGroup <= 0 || sourceGroup > 255 {
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	group, supported := state.Config.CollectionGroupFor(job, branch)
	if !supported {
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	character, err := adventuregroup.SelectCollection(ctx, state.Repositories, adventuregroup.SelectCollectionCommand{
		AccountID: state.AccountID, CharacterID: strconv.FormatUint(uint64(binary.LittleEndian.Uint32(request[1:5])), 10),
		Job: job, Branch: branch, ServerGroup: request[0], DefaultServerGroup: byte(sourceGroup), LevelCap: currentAdventureCharacterLevelCap, UpdatedAt: s.gameplayNow(),
	})
	if err != nil {
		s.logGameEvent(session, "game-adventure-represent-select-rejected", "error", err)
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	account, found, err := state.Repositories.Account.Load(ctx, state.AccountID)
	if err != nil || !found {
		return s.sendGameUpperFailure(session, opcode, currentAdventureGenericFailureCode)
	}
	rows := adventuregroup.CollectionRows(state.Characters, adventuregroup.CollectionSelections(account), currentAdventureCharacterLevelCap)
	counts := state.Config.CollectionCounts(rows, currentAdventureCharacterLevelCap)
	entry := make([]byte, currentAdventureInfoRosterEntrySize)
	writeCurrentAdventureInfoRosterEntry(entry, character, byte(sourceGroup))
	var writer packetWriter
	writer.writeUint32(3)
	writer.writeByte(group)
	writer.writeUint16(counts[group])
	writer.writeUint32(uint32(len(entry)))
	writer.writeBytes(entry)
	s.logGameEvent(session, "game-adventure-represent-select-saved", "character_id", character.CharacterID, "job", job, "grow", branch, "collection_group", group, "count", counts[group])
	if err := s.sendGameUpperSuccess(session, opcode, writer.bytes()); err != nil {
		return err
	}
	// E63C40 installs the selected collection row but never replaces the
	// summary header at manager +EC/+F4. The existing class0/op1339 reader
	// E634CB/E634E3 installs that header without opening another window;
	// BEE108/BEE120 then uses it when the basic-information tab is displayed.
	// Refresh from the committed account selection, after its success ACK.
	return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "collection_representative_selected")
}

func currentAdventureCharacterServerGroup(character dnfrepo.CharacterRecord) byte {
	value := numericCharacterStatValue(character, "server_group_id")
	if value < 0 || value > 255 {
		return 0
	}
	return byte(value)
}
