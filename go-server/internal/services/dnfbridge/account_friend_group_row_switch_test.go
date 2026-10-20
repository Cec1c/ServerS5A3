package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// parseAccountFriendInfoRows decodes a 1371 body exactly as built by
// buildCurrentAccountFriendInfoBody, so the assertion covers the real wire
// layout (head, state, adventure name, level/job/grow, channel, character name,
// remark, timestamp, flag).
func parseAccountFriendInfoRows(t *testing.T, body []byte) []currentAccountFriendNativeRow {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("empty 1371 body")
	}
	count := int(body[0])
	offset := 1
	rows := make([]currentAccountFriendNativeRow, 0, count)
	for i := 0; i < count; i++ {
		if offset+3 > len(body) {
			t.Fatalf("row %d: short head", i)
		}
		head := uint16(body[offset]) | uint16(body[offset+1])<<8
		state := body[offset+2]
		offset += 3
		advRaw, end, ok := readCurrentRawDstr(body, offset)
		if !ok {
			t.Fatalf("row %d: adventure name", i)
		}
		offset = end
		adv, err := decodeCharacterName(advRaw)
		if err != nil {
			t.Fatalf("row %d: decode adventure name: %v", i, err)
		}
		if offset+5 > len(body) {
			t.Fatalf("row %d: short stats", i)
		}
		level, job, grow := body[offset], body[offset+1], body[offset+2]
		channel := uint16(body[offset+3]) | uint16(body[offset+4])<<8
		offset += 5
		charRaw, end, ok := readCurrentRawDstr(body, offset)
		if !ok {
			t.Fatalf("row %d: character name", i)
		}
		offset = end
		name, err := decodeCharacterName(charRaw)
		if err != nil {
			t.Fatalf("row %d: decode character name: %v", i, err)
		}
		remarkRaw, end, ok := readCurrentRawDstr(body, offset)
		if !ok {
			t.Fatalf("row %d: remark", i)
		}
		offset = end
		remark := ""
		if len(remarkRaw) > 0 {
			if remark, err = decodeCharacterName(remarkRaw); err != nil {
				t.Fatalf("row %d: decode remark: %v", i, err)
			}
		}
		if offset+5 > len(body) {
			t.Fatalf("row %d: short tail", i)
		}
		offset += 4
		flag := body[offset] != 0
		offset++
		rows = append(rows, currentAccountFriendNativeRow{
			Head: head, NeutralState: state, AdventureName: adv,
			Level: level, Job: job, Grow: grow, Channel: channel,
			CharacterName: name, Remark: remark, NeutralFlag: flag,
		})
	}
	if offset != len(body) {
		t.Fatalf("1371 body trailing bytes: consumed %d of %d", offset, len(body))
	}
	return rows
}

// D33 P57: one friend adventure group is exactly one top-level row. When the
// representative character changes the server must remove the vanished name and
// upsert the new one, so the client keeps a single row instead of growing one row
// per online character.
func TestAccountFriendGroupRowSwitchesRepresentativeWith1373Then1371(t *testing.T) {
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
	_ = g
	_ = ac
	_ = bc

	// Before the switch the only online character of dnf:2 is 来来来 (id 5), so the
	// session cache holds exactly that one row name.
	if _, exists := a.currentAccountFriendNames["来来来"]; !exists {
		t.Fatalf("baseline row name not cached: %v", a.currentAccountFriendNames)
	}

	// 小号 (id 8) logs in and is the account's durable last-selected character: the
	// single row must follow it instead of adding a second row.
	second := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", selectedCharacterID: 8, loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 7}}
	s.bindGameSessionCharacter(second, 8)
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "8")

	rows, pending, err := s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(pending) != 0 {
		t.Fatalf("projection: rows=%+v pending=%+v err=%v", rows, pending, err)
	}
	if len(rows) != 1 {
		t.Fatalf("one friend adventure group must be one row: rows=%+v", rows)
	}
	body, err := buildCurrentAccountFriendInfoBody(rows)
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseAccountFriendInfoRows(t, body)
	if len(parsed) != 1 {
		t.Fatalf("1371 must carry one row per adventure group: %+v", parsed)
	}
	got := parsed[0]
	if got.AdventureName != "Beta" || got.CharacterName != "小号" || got.Channel != 7 {
		t.Fatalf("1371 row identity: %+v", got)
	}

	// Real wire ordering on the friend owner: the vanished representative is
	// removed (1373) before the new one is upserted (1371), then pending (1372).
	ac.write.Reset()
	if err := s.sendCurrentAccountFriendLists(a, "presence"); err != nil {
		t.Fatal(err)
	}
	ids := accountFriendPacketIDs(t, ac)
	if len(ids) != 3 || ids[0] != 1373 || ids[1] != 1371 || ids[2] != 1372 {
		t.Fatalf("representative switch packet order: %v", ids)
	}
	removal, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
	removedRaw, _, ok := readCurrentRawDstr(removal.Body, 0)
	if !ok {
		t.Fatalf("1373 body: %+v", removal)
	}
	removed, err := decodeCharacterName(removedRaw)
	if err != nil || removed != "来来来" {
		t.Fatalf("1373 must name the vanished row: %q err=%v", removed, err)
	}
	if len(a.currentAccountFriendNames) != 1 {
		t.Fatalf("session row cache must hold one name: %v", a.currentAccountFriendNames)
	}
	if _, exists := a.currentAccountFriendNames["小号"]; !exists {
		t.Fatalf("session row cache must hold the new representative: %v", a.currentAccountFriendNames)
	}
}
