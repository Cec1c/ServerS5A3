package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/config"
)

func TestMainQuestExperienceMultiplierOptionDefaultsAndValidation(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
	}{
		{raw: "", want: 1},
		{raw: "3", want: 3},
		{raw: " 100 ", want: 100},
		{raw: "not-an-integer", want: 1},
		{raw: "0", want: 1},
		{raw: "-1", want: 1},
		{raw: "101", want: 1},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv(mainQuestExperienceMultiplierEnv, test.raw)
			if got := optionsFromConfig(config.ServiceConfig{}).mainQuestExperienceMultiplier; got != test.want {
				t.Fatalf("mainQuestExperienceMultiplier(%q)=%d, want %d", test.raw, got, test.want)
			}
		})
	}
}

func TestCurrentFinishQuestPassesTrustedMainQuestExperienceMultiplier(t *testing.T) {
	repositories := finishBridgeSeed(t)
	connection := &bufferConn{}
	catalog := finishBridgeCatalog(t)
	service := &Service{
		options: options{
			gameUpperBodyCodec:            gameUpperBodyCodecPlain,
			mainQuestExperienceMultiplier: 3,
		},
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "finish-main-quest-exp", selectedCharacterID: 19}
	request := dnfquest.FinishQuestRequest{
		QuestID:           3145,
		RewardSelectIndex: ^uint16(0),
		Multiplier:        1,
		Reserved:          dnfquest.CurrentFinishQuestObservedTailMarker,
	}
	if err := service.handleCurrentFinishQuestWithResources(
		session,
		request,
		repositories,
		catalog,
		finishBridgeProgression(t),
		finishBridgeAllocator(nil),
	); err != nil {
		t.Fatal(err)
	}
	packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
	if len(packets) == 0 ||
		packets[0].Header.MsgID != uint16(dnfenum.CmdPacketFinishQuest) ||
		len(packets[0].Body) < 8 ||
		binary.LittleEndian.Uint32(packets[0].Body[4:8]) != 330 {
		t.Fatalf("trusted main quest multiplier finish ACK=%+v", packets)
	}
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found || character.Stats["exp"] != 330 {
		t.Fatalf("trusted main quest multiplier character=%+v found=%t err=%v", character, found, err)
	}
}
