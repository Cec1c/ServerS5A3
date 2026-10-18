package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGuardianShieldExpandedProjectionWaitsForActorAndRefreshesCombatPower(t *testing.T) {
	service := &Service{}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 77}
	err := service.sendAlignedPostAction(session, alignedcmd.Result{Operation: "guardian_shield_move"}, alignedcmd.PostActionRefreshSelectedActorExpandedEquipment)
	if !errors.Is(err, errAlignedPostCommitProjectionDeferred) || conn.write.Len() != 0 {
		t.Fatalf("场景未就绪时不应发盾牌外观: err=%v bytes=%x", err, conn.write.Bytes())
	}
	if !alignedPostActionTouchesCombatPower(alignedcmd.PostActionRefreshSelectedActorExpandedEquipment) {
		t.Fatal("主盾卸下后缺少战力刷新")
	}
}

func TestGuardianShieldLoginPublishesFiveSlotsOnceAndOtherJobsStaySilent(t *testing.T) {
	for _, job := range []string{"12", "0"} {
		t.Run(job, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: job, Stats: map[string]int64{"grow_type": 1}}); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			service := &Service{options: options{accountID: "acc", gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, accountID: "acc", selectedCharacterID: 77}
			for i := 0; i < 2; i++ {
				if _, err := service.sendSelectedGuardianShieldDeckInfo(session, "test"); err != nil {
					t.Fatal(err)
				}
			}
			if job != "12" {
				if conn.write.Len() != 0 {
					t.Fatal("其它职业收到盾牌数据")
				}
				return
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(rest) != 0 || packet.Header.Classification != 0 || packet.Header.MsgID != 580 || len(packet.Body) != 20 {
				t.Fatalf("盾牌通知形状不符: %+v body=%x rest=%x", packet.Header, packet.Body, rest)
			}
		})
	}
}

func TestGuardianShieldMode0AppearanceAppendsOnlyOwnedMainProjection(t *testing.T) {
	entry := dnfguardianshield.BuildMainProjectionEntry(113370003, alignedcmd.GuardianShieldResolution{ItemID: 113370003})
	for _, marked := range []bool{true, false} {
		if !marked {
			entry.Extra = nil
		}
		rows, err := buildCurrentActorMode0AppearanceSummaryFromEquipment(dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{"24": entry}})
		if err != nil {
			t.Fatal(err)
		}
		want := 14
		if marked {
			want++
		}
		if len(rows) != want {
			t.Fatalf("marked=%t rows=%d want=%d", marked, len(rows), want)
		}
		if marked && (rows[14].Slot != 24 || rows[14].ItemIDOrIcon != 113370003) {
			t.Fatalf("主盾外观不符: %+v", rows[14])
		}
	}
}
