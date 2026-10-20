package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/config"
)

func TestMainQuestExperiencePercentOptionDefaultsAndValidation(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
	}{
		{raw: "", want: 0},
		{raw: "0", want: 0},
		{raw: "1", want: 1},
		{raw: "15", want: 15},
		{raw: " 100 ", want: 100},
		{raw: "not-an-integer", want: mainQuestExperiencePercentInvalid},
		{raw: "-1", want: mainQuestExperiencePercentInvalid},
		{raw: "101", want: mainQuestExperiencePercentInvalid},
		{raw: "15.0", want: mainQuestExperiencePercentInvalid},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv(mainQuestExperiencePercentEnv, test.raw)
			if got := optionsFromConfig(config.ServiceConfig{}).mainQuestExperiencePercent; got != test.want {
				t.Fatalf("mainQuestExperiencePercent(%q)=%d, want %d", test.raw, got, test.want)
			}
		})
	}
}

func TestCurrentFinishQuestPassesTrustedMainQuestExperiencePercent(t *testing.T) {
	repositories := finishBridgeSeed(t)
	connection := &bufferConn{}
	catalog := finishBridgeCatalog(t)
	service := &Service{
		options: options{
			gameUpperBodyCodec:         gameUpperBodyCodecPlain,
			mainQuestExperiencePercent: 15,
		},
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "finish-main-quest-percent", selectedCharacterID: 19}
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
		binary.LittleEndian.Uint32(packets[0].Body[4:8]) != 15 {
		t.Fatalf("trusted main quest percentage finish ACK=%+v", packets)
	}
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found || character.Stats["exp"] != 15 {
		t.Fatalf("trusted main quest percentage character=%+v found=%t err=%v", character, found, err)
	}
}

func TestInvalidMainQuestExperiencePercentIsBusinessFailure(t *testing.T) {
	if !currentFinishQuestBusinessFailure(dnfquest.ErrFinishMainQuestExperiencePercentInvalid) {
		t.Fatal("invalid percentage must use the existing op34 business failure reply")
	}
	if !currentFinishQuestBusinessFailure(dnfquest.ErrFinishMainQuestExperiencePercentLevelInvalid) {
		t.Fatal("invalid quest level must use the existing op34 business failure reply")
	}
	if currentFinishQuestBusinessFailure(errors.New("unclassified")) {
		t.Fatal("unclassified error was treated as an op34 business failure")
	}
}
