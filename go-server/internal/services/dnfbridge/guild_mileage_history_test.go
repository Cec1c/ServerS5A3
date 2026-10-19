package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
)

func TestGuildMileageHistoryCommittedPurchaseThroughStream(t *testing.T) {
	s, peers, _, _ := guildBenefitsFixture(t)
	sendGuildPurchase(t, s, peers[0], 2, 30)
	sendGuildPurchase(t, s, peers[0], 2, 30) // Same receipt must not add history twice.
	wire, _ := protocol.BuildChannelPacket(747, nil, 31, 1)
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatal("invalid history stream")
	}
	if err := s.handleGameStreamPacket(peers[0], packets[0]); err != nil {
		t.Fatal(err)
	}
	body := guildReply(t, peers[0], 747)
	if len(body) != 53 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:5]) != 1 {
		t.Fatalf("purchase history=%x", body)
	}
	row := body[5:]
	if row[34] != 5 || row[35] != 0 || binary.LittleEndian.Uint32(row[36:40]) != 2 || binary.LittleEndian.Uint32(row[40:44]) != 200 {
		t.Fatalf("native content expense=%x", row)
	}
}

func TestGuildMileageHistoryIncomeExpenseNativeKinds(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, entry := range []dnfguild.MileageHistoryEntry{
		{At: now, CharacterName: "通关者", Kind: 3, Amount: 10},
		{At: now, CharacterName: "购买者", Kind: 5, ContentID: 11, Amount: 10000},
		{At: now, CharacterName: "使用者", Kind: 8, Amount: 100},
	} {
		body, err := buildCurrentGuildMileageHistoryBody([]dnfguild.MileageHistoryEntry{entry})
		if err != nil || len(body) != 52 || body[38] != entry.Kind || binary.LittleEndian.Uint32(body[40:44]) != entry.ContentID || binary.LittleEndian.Uint32(body[44:48]) != entry.Amount {
			t.Fatalf("entry=%+v body=%x err=%v", entry, body, err)
		}
	}
	for _, entry := range []dnfguild.MileageHistoryEntry{
		{At: now, CharacterName: "test", Kind: 5, Amount: 10},
		{At: now, CharacterName: "test", Kind: 3, ContentID: 11, Amount: 10},
		{At: now, CharacterName: "test", Kind: 4, Amount: 10},
		{At: now, CharacterName: "test", Kind: 3, Amount: math.MaxInt32 + 1},
	} {
		if _, err := buildCurrentGuildMileageHistoryBody([]dnfguild.MileageHistoryEntry{entry}); err == nil {
			t.Fatalf("invalid native row accepted: %+v", entry)
		}
	}
}

func TestGuildMileageHistoryNativeNameAndOrdering(t *testing.T) {
	now := time.Unix(1700000000, 0)
	rows := []dnfguild.MileageHistoryEntry{
		{At: now, CharacterName: "older", Kind: 8, Amount: 100},
		{At: now.Add(time.Second), CharacterName: strings.Repeat("会", 15), Kind: 8, Amount: 200},
	}
	body, err := buildCurrentGuildMileageHistoryBody(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 100 || binary.LittleEndian.Uint32(body[:4]) != 2 {
		t.Fatalf("body=%x", body)
	}
	row := body[4:52]
	if binary.LittleEndian.Uint32(row[:4]) != uint32(now.Unix()+1) || !bytes.Equal(row[4:32], rosterNameBytes(strings.Repeat("会", 14))) || row[32] != 0 || row[33] != 0 || row[34] != 8 || binary.LittleEndian.Uint32(row[40:44]) != 200 {
		t.Fatalf("newest row/terminated GB18030 name=%x", row)
	}
	if binary.LittleEndian.Uint32(body[52:56]) != uint32(now.Unix()) {
		t.Fatal("history not newest first")
	}
}

func TestGuildMileageHistoryIncludesCommittedBagUse(t *testing.T) {
	s, session, repos := guildMileageFixture(t)
	ctx := context.Background()
	if handled, err := s.handleAlignedGameCommand(session, 1, 515, testQuestClearCouponRequestBody(87, 145)); err != nil || !handled {
		t.Fatalf("use handled=%v err=%v", handled, err)
	}
	session.conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, session, 747, nil)
	body := guildReply(t, session, 747)
	if len(body) != 53 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:5]) != 1 {
		t.Fatalf("history header/size=%x", body)
	}
	row := body[5:]
	character, _, _ := repos.Character.Load(ctx, "29")
	if row[34] != 8 || binary.LittleEndian.Uint32(row[40:44]) != 100 || !bytes.Equal(bytes.TrimRight(row[4:34], "\x00"), rosterNameBytes(character.Name)) {
		t.Fatalf("native row=%x", row)
	}
	if binary.LittleEndian.Uint32(row[:4]) == 0 || !bytes.Equal(row[35:40], make([]byte, 5)) || !bytes.Equal(row[44:], make([]byte, 4)) {
		t.Fatalf("time or reserved fields=%x", row)
	}
	// A member on another channel receives the same guild history; another
	// guild's panel stays empty. Query bodies cannot select a different guild.
	peer := s.gameSessions[33]
	guildDispatch(t, s, peer, 747, nil)
	if !bytes.Equal(guildReply(t, peer, 747), body) {
		t.Fatal("same guild history differs across channels")
	}
	other := s.gameSessions[31]
	guildDispatch(t, s, other, 747, nil)
	if !bytes.Equal(guildReply(t, other, 747), []byte{1, 0, 0, 0, 0}) {
		t.Fatal("another guild received source history")
	}
}

func TestGuildMileageHistoryRejectsUnauthorizedAndMalformedQueries(t *testing.T) {
	for _, mode := range []string{"nonmember", "wrong_account", "stale_session", "foreign_body", "member_account"} {
		t.Run(mode, func(t *testing.T) {
			s, session, repos := guildMileageFixture(t)
			var body []byte
			switch mode {
			case "nonmember":
				session = s.gameSessions[32]
			case "wrong_account":
				session.accountID = "other"
			case "stale_session":
				s.gameSessions[29] = &gameSession{selectedCharacterID: 29, characterGeneration: 99}
			case "foreign_body":
				body = []byte{1, 0, 0, 0}
			case "member_account":
				r, _ := dnfguild.Load(context.Background(), repos.Settings)
				g, m, _ := r.GuildForCharacter("29")
				m.AccountID = "other"
				g.Members["29"] = m
				if err := dnfguild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			guildDispatch(t, s, session, 747, body)
			if got := guildReply(t, session, 747); !bytes.Equal(got, []byte{0, currentGuildGenericError}) {
				t.Fatalf("rejection=%x", got)
			}
		})
	}
}

func TestGuildMileageHistoryDoesNotInventOldBalanceEvents(t *testing.T) {
	s, session, repos := guildMileageFixture(t)
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, _, _ := r.GuildForCharacter("29")
	g.Mileage = 9999
	if err := dnfguild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	guildDispatch(t, s, session, 747, nil)
	if got := guildReply(t, session, 747); !bytes.Equal(got, []byte{1, 0, 0, 0, 0}) {
		t.Fatalf("fabricated old history=%x", got)
	}
}
