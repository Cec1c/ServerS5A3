package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPairedPVFSaintQuestLegacyGiveUpPreservesProfession(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.Find(2908)
	if !ok || definition.CantGiveUp || definition.HasDependGiveItem || len(definition.MonsterRewardItems) != 0 || len(definition.EnemyRewardItems) != 0 {
		t.Fatal("paired quest is missing or requires another abandon contract")
	}
	repositories := memory.NewMemoryGroup()
	character := repo.CharacterRecord{CharacterID: "17", AccountID: defaultAccountPrefix + "1", Level: 90, Job: "14", Stats: map[string]int64{"grow_type": 2, "exp": 123}}
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, repo.QuestRecord{CharacterID: "17", States: map[int64]repo.QuestState{2908: {Status: "active", ProgressValue: 1}, 3155: {Status: "completed"}}, Progress: map[int64]repo.QuestState{2908: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return repositories, true }}
	session := &gameSession{conn: connection, selectedCharacterID: 17}
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketGiveupQuest), []byte{0x20, 0, 0x5c, 0x0b}); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := repositories.Quest.Load(ctx, "17")
	if _, present := stored.States[2908]; present {
		t.Fatal("Saint quest remains active")
	}
	if _, present := stored.Progress[2908]; present {
		t.Fatal("Saint progress remains")
	}
	if stored.States[3155].Status != "completed" {
		t.Fatal("other completed quest changed")
	}
	after, _, _ := repositories.Character.Load(ctx, "17")
	if after.Job != character.Job || after.Level != 90 || after.Stats["grow_type"] != 2 || after.Stats["exp"] != 123 {
		t.Fatal("abandon changed profession or progression")
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 32 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1, 0x5c, 0x0b}) {
		t.Fatalf("ack=%+v body=%x", packet.Header, packet.Body)
	}
}
