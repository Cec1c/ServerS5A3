package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/mail"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailboxPureGoldClaimSendsCommittedGoldAfterAckWithoutRelog(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": 10}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: "77", Mails: map[string]repo.MailRecord{"123": {MailID: "123", Gold: 50}}}); err != nil {
		t.Fatal(err)
	}
	body := []byte{1, 0, 0, 0, 123, 0, 0, 0}
	req := alignedcmd.Request{Opcode: 95, SelectedCharacterID: 77, Repositories: r, Body: body}
	result, err := mail.NewHandler().Handle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CharacterGoldChanged || result.AccountWalletChanged || len(result.PostActions) != 0 || len(result.ItemSlotRefreshes) != 0 {
		t.Fatalf("claim projections: %+v", result)
	}
	conn := &bufferConn{}
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	p := &gameSession{conn: conn, selectedCharacterID: 77}
	s.bindGameSessionCharacter(p, 77)
	if err := s.sendAlignedUpperResponses(p, result); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 95 || ack.Header.Classification != 1 {
		t.Fatalf("first response=%+v", ack.Header)
	}
	gold, tail := splitGameServerUpperPacket(t, rest)
	// Native list0, one 0x77-byte row: slot0/item0/count60. No full op13 or mailbox rebuild.
	want := make([]byte, 3+0x77)
	want[1] = 1
	binary.LittleEndian.PutUint32(want[3+6:], 60)
	if gold.Header.MsgID != 14 || gold.Header.Classification != 0 || !bytes.Equal(gold.Body, want) || len(tail) != 0 {
		t.Fatalf("gold packet=%+v body=%x tail=%x", gold.Header, gold.Body, tail)
	}
	again, err := mail.NewHandler().Handle(ctx, req)
	if err != nil || again.CharacterGoldChanged {
		t.Fatalf("duplicate result=%+v err=%v", again, err)
	}
	c, _, _ := r.Character.Load(ctx, "77")
	if c.Stats["gold"] != 60 {
		t.Fatalf("duplicate gold=%d", c.Stats["gold"])
	}
}

type goldRefreshRebindingCharacters struct {
	repo.CharacterRepository
	afterRead func()
}

func (r *goldRefreshRebindingCharacters) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	c, found, err := r.CharacterRepository.Load(ctx, id)
	r.afterRead()
	return c, found, err
}

func TestMailboxGoldRefreshDropsBindingRetiredDuringRead(t *testing.T) {
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": 60}}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	p := &gameSession{conn: conn}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	s.bindGameSessionCharacter(p, 77)
	r.Character = &goldRefreshRebindingCharacters{CharacterRepository: r.Character, afterRead: func() { s.bindGameSessionCharacter(p, 78) }}
	if err := s.sendCommittedCharacterGold(p); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("old character balance reached the replacement character")
	}
}
