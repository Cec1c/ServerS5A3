package dnfbridge

import (
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestPVPEntryEquipmentExistsWhenSceneSnapshotChecksWornSlots(t *testing.T) {
	for _, pvp := range []bool{false, true} {
		stream, commit, _, _, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{pvpChannel: pvp})
		snapshot, full, count := -1, -1, 0
		for _, frame := range loginAuthoritativeSplitAll(t, stream) {
			if frame.msgID == currentTownActorSceneSnapshotMsgID {
				snapshot = frame.offset
				count++
			}
			if frame.msgID == 2 && frame.mode == 1 {
				rows, _, _, _, weapon, ok := loginActorGenerationMode1WireRowAudit(frame.body)
				if ok && rows == 31 && weapon[1] == "present" {
					full = frame.offset
				}
			}
		}
		if count != 1 || full < 0 || snapshot < 0 || snapshot >= commit || full >= commit {
			t.Fatalf("入口载体缺失: 无双%t snapshot=%d full=%d commit=%d count=%d", pvp, snapshot, full, commit, count)
		}
		if pvp && snapshot < full {
			t.Fatal("无双入口在空装备绑定帧后提前触发穿戴检查")
		}
		if !pvp && snapshot > full {
			t.Fatal("普通频道的既有场景顺序改变")
		}
	}
}

func TestPVPAllChannelEntriesCheckEquipmentAfterFullWornState(t *testing.T) {
	channels := []channelcatalog.Channel{
		{ID: 75, Type: 8, Group: "pvp"},
		{ID: 191, Type: 8, Group: "pvp"},
	}
	for id := 192; id <= 197; id++ {
		channels = append(channels, channelcatalog.Channel{ID: id, Type: 24, Group: "pvp"})
	}
	// 同编号或同type但不是PK分组时，保留普通城镇的既有顺序。
	channels = append(channels, channelcatalog.Channel{ID: 192, Type: 24, Group: "normal"})
	for _, channel := range channels {
		t.Run(fmt.Sprintf("%s_%d", channel.Group, channel.ID), func(t *testing.T) {
			stream, commit, _, _, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{channelOverride: &channel})
			full, snapshot, count := -1, -1, 0
			for _, frame := range loginAuthoritativeSplitAll(t, stream) {
				if frame.msgID == currentTownActorSceneSnapshotMsgID {
					snapshot = frame.offset
					count++
					if len(frame.body) != 4 || frame.body[0] != 1 || frame.body[1] != 6 || frame.body[2] != 0 || frame.body[3] != 0 {
						t.Fatalf("场景快照载体发生变化：%x", frame.body)
					}
				}
				if frame.msgID == 2 && frame.mode == 1 {
					rows, _, _, _, weapon, ok := loginActorGenerationMode1WireRowAudit(frame.body)
					if ok && rows == 31 && weapon[1] == "present" {
						full = frame.offset
					}
				}
			}
			if count != 1 || full < 0 || snapshot < 0 || snapshot >= commit || full >= commit {
				t.Fatalf("入口装备或快照缺失：full=%d snapshot=%d commit=%d count=%d", full, snapshot, commit, count)
			}
			if isPVPChannel(channel) && snapshot < full {
				t.Fatalf("PK入口先检查空装备，完整装备晚到：snapshot=%d full=%d", snapshot, full)
			}
			if !isPVPChannel(channel) && snapshot > full {
				t.Fatal("普通频道的场景顺序改变")
			}
		})
	}
}
