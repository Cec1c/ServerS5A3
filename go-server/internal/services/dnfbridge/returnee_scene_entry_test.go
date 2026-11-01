package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	event "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestReturneeSceneEntryColdQualificationAndNativeByte(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Now()
	state := event.ReturneeState{Started: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), UserType: 5}
	raw, _ := json.Marshal(state)
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{event.ReturneeMetadataKey: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	character := repo.CharacterRecord{AccountID: "a", CharacterID: "7", Level: 90, Stats: map[string]int64{"skill_tree_expanded": 1, "skill_tree_index": 1}}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }}
	q := &gameSession{ctx: ctx, accountID: "a", selectedCharacterID: 7, characterGeneration: 3}
	enablePortedActivityTest(t, s, q)
	s.returneeCatalog = &event.ReturneeCatalog{}
	s.luckyCharacterCatalog = &currentLuckyCatalog{}
	projected, err := s.currentReturneeSceneCharacter(q, 7, character, true)
	if err != nil || currentReturneeSceneFlag(projected, true) != 1 || q.returneePreparedGeneration != 3 {
		t.Fatal("首个场景前未初始化真实资格", projected, err)
	}
	if _, exists := character.Stats[currentReturneeSceneFlagKey]; exists {
		t.Fatal("报文投影修改了原始Stats")
	}
	saved, _, _ := g.Character.Load(ctx, "7")
	if _, exists := saved.Stats[currentReturneeSceneFlagKey]; exists {
		t.Fatal("临时显示位落入角色存储")
	}
	for _, name := range []string{"", "冒险团"} {
		var before, after packetWriter
		writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(&before, character, true, currentEquippedCreatureSnapshot{}, name)
		writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(&after, projected, true, currentEquippedCreatureSnapshot{}, name)
		a, b := before.bytes(), after.bytes()
		// 22c6458消费技能页；22c646d消费回归u8；其后仍为u32。
		p := len(a) - 5
		if len(a) != len(b) || a[p] != 0 || b[p] != 1 || a[p-1] != 1 || b[p-1] != 1 || !bytes.Equal(a[:p], b[:p]) || !bytes.Equal(a[p+1:], b[p+1:]) {
			t.Fatalf("回归位移动了相邻字段: %x / %x", a, b)
		}
	}
	for _, test := range []string{"过期", "换角代际", "其他账号", "其他角色", "无角色", "旧协议"} {
		t.Run(test, func(t *testing.T) {
			// 子测试顺序执行；沿用真实会话身份，只恢复本例调整的标量。
			local := q
			selected, generation, expiry := q.selectedCharacterID, q.characterGeneration, q.returneePreparedExpiresAt
			defer func() {
				q.selectedCharacterID, q.characterGeneration, q.returneePreparedExpiresAt = selected, generation, expiry
			}()
			c := repo.CloneCharacter(projected)
			has := true
			switch test {
			case "过期":
				local.returneePreparedExpiresAt = time.Now().Add(-time.Second).Unix()
			case "换角代际":
				local.selectedCharacterID = 8
				local.characterGeneration++
			case "其他账号":
				c.AccountID = "b"
			case "其他角色":
				c.CharacterID = "8"
			case "无角色":
				has = false
			case "旧协议":
				s.options.currentClientVersion = "legacy"
				defer func() { s.options.currentClientVersion = "official-24.9" }()
			}
			got, err := s.currentReturneeSceneCharacter(local, 7, c, has)
			if err != nil || currentReturneeSceneFlag(got, true) != 0 {
				t.Fatal("无资格场景继承回归入口", got, err)
			}
		})
	}
	if _, err := s.activityControls.Save(ctx, s.activityControls.Snapshot().Revision, []event.ControlChange{{ID: 1370, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.currentReturneeSceneCharacter(q, 7, projected, true)
	if err != nil || currentReturneeSceneFlag(got, true) != 0 {
		t.Fatal("关闭活动后保留入口", err)
	}
}
