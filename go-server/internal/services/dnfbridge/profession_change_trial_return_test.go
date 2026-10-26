package dnfbridge

import (
	"reflect"
	"testing"
)

func TestProfessionTrialReturnConfirmationKeepsOrdinaryDungeonPending(t *testing.T) {
	for _, systemMenu := range []bool{false, true} {
		name := "普通副本放弃"
		if systemMenu {
			name = "普通副本系统返回"
		}
		t.Run(name, func(t *testing.T) {
			service, session, runtime := newBackToVillageRuntime(t)
			var err error
			if systemMenu {
				err = service.handleDungeonBackToVillage(session, nil)
			} else {
				err = service.handleDungeonGiveupGame(session, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := runtime.Session.Snapshot()
			if !runtime.townReturnPending || !runtime.townReturnOp24Sent {
				t.Fatal("夹具没有真实发送返回城镇")
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			if err := service.confirmCurrentProfessionTrialTownReturn(session); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != runtime || !runtime.townReturnPending || !runtime.townReturnOp24Sent || !reflect.DeepEqual(before, runtime.Session.Snapshot()) || conn.write.Len() != 0 {
				t.Fatal("转职入口确认了其它副本的待决返回")
			}
		})
	}
}

func TestProfessionTrialReturnConfirmationRejectsReplacedRuntime(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	before := runtime.Session.Snapshot()
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := service.commitPendingDungeonReturnForExpectedSceneRequest(session, "确认原体验实例", &runtimeDungeonState{}); err == nil {
		t.Fatal("错误副本持有者仍可确认当前返回")
	}
	if session.dungeon.runtime != runtime || !runtime.townReturnPending || !reflect.DeepEqual(before, runtime.Session.Snapshot()) || conn.write.Len() != 0 {
		t.Fatal("副本归属不符时仍修改或投影了当前场景")
	}
}

func TestProfessionTrialReturnProfileRestoresOrdinaryTownGeneration(t *testing.T) {
	service, session, _ := newBackToVillageRuntime(t)
	service.options.gameUpperHeader = gameUpperHeaderServer16
	conn := session.conn.(*bufferConn)
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "普通副本确认回城应保留激活"); err != nil {
		t.Fatal(err)
	}
	ordinary := []string{"ack42", "mode0", "mode1", "op105", "op37", "op30", "op19", "op120"}
	if got := professionTrialReturnPacketSignatures(t, splitProfessionTrialServer16Packets(t, conn.write.Bytes())); !reflect.DeepEqual(got, ordinary) {
		t.Fatalf("普通副本回城被省略策略影响：got=%v want=%v", got, ordinary)
	}
	if session.townPostTransition.omitSceneActivation || !session.currentFinishLoadingCompletionSent {
		t.Fatal("普通副本没有真实发送并记录op30")
	}

	// 单独验证代数生命周期；7142/42策略的生产准入由真实PVF专项走完整入口验证。
	// 使用生产arm方法建立省略代，避免直接篡改阶段或把未发送的激活标为成功。
	conn.write.Reset()
	service.armCurrentTownPostTransitionPlayerStateWithProfile(session, "验证省略代生命周期", session.selectedCharacterID, currentConnectionTownActorOwnerContext(session), true)
	omittedGeneration := session.townPostTransition.generation
	if err := service.sendCurrentTownPostTransitionPlayerState(session, "发送省略代"); err != nil {
		t.Fatal(err)
	}
	wantOmitted := []string{"mode0", "mode1", "op105", "op37", "op19", "op120"}
	if got := professionTrialReturnPacketSignatures(t, splitProfessionTrialServer16Packets(t, conn.write.Bytes())); !reflect.DeepEqual(got, wantOmitted) {
		t.Fatalf("省略代没有只去掉op30：got=%v want=%v", got, wantOmitted)
	}
	if session.currentFinishLoadingCompletionSent || !currentTownReturnOmitsSceneActivation(session) {
		t.Fatal("省略代伪标激活完成或丢失同代策略")
	}

	conn.write.Reset()
	service.armCurrentTownPostTransitionPlayerState(session, "下一次普通城镇过场")
	if session.townPostTransition.generation <= omittedGeneration || session.townPostTransition.omitSceneActivation || currentTownReturnOmitsSceneActivation(session) {
		t.Fatal("普通城镇新代沿用了上代7142的省略策略")
	}
	if err := service.sendCurrentTownPostTransitionPlayerState(session, "发送普通城镇新代"); err != nil {
		t.Fatal(err)
	}
	wantOrdinary := ordinary[1:]
	if got := professionTrialReturnPacketSignatures(t, splitProfessionTrialServer16Packets(t, conn.write.Bytes())); !reflect.DeepEqual(got, wantOrdinary) {
		t.Fatalf("普通城镇新代未恢复完整激活链：got=%v want=%v", got, wantOrdinary)
	}
	if !session.currentFinishLoadingCompletionSent {
		t.Fatal("新城镇代op30发送成功后没有记录完成")
	}
}
