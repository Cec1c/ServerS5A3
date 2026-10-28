package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 复现现场一个胶囊后进度恒零：真实PVF、连续击杀、账号重载、换未满级角色领取。
func TestRealPVFAdventureShopExperienceSynchronizesAcrossCapsules(t *testing.T) {
	a := real90DropArchive(t)
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	ctx := context.Background()
	var err error
	s.initialEquipmentArchive = a
	s.adventureGroupTable, err = adventuregroup.LoadComplete(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	s.honorTable, err = dnfhonor.LoadTables(a)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	config := s.adventureGroupTable.Runtime()
	if config.Capsule.MinimumExperience != 11_691_495 || config.Capsule.MaximumExperience != 116_914_950 || config.Capsule.MaximumCount != 10 {
		t.Fatalf("现用PVF胶囊规则发生变化：%+v", config.Capsule)
	}
	brave, found := config.Shop(adventuregroup.ShopPointBrave)
	if !found || brave.ExperiencePerPoint != 15_000_000 {
		t.Fatalf("真实PVF骁勇结晶换算不同：%+v", brave)
	}
	state := adventuregroup.RuntimeState{GrowthExperience: config.Capsule.MaximumExperience, BraveExperience: 5_131_134, ShopPoints: [3]uint32{25, 4, 0}}
	state.Normalize(config, s.gameplayNow())
	encoded, err := state.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{adventuregroup.RuntimeStateMetadataKey: encoded}}); err != nil {
		t.Fatal(err)
	}
	definition, found, err := s.dungeonMonsterTable.Find(107005202)
	if err != nil || !found {
		t.Fatalf("真实怪物加载失败：%v", err)
	}
	r := p.dungeon.runtime
	for i := range r.Room.monsters {
		m := &r.Room.monsters[i]
		m.Spawn.MonsterID, m.Spawn.Level, m.Spawn.AutoLevel = 107005202, 0, 85
		m.Definition = definition
	}
	resources, err := currentDungeonMonsterExperienceResourcesForCatalog(s.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	award, err := currentDungeonMonsterExperienceAwardFor(r, r.Room.monsters[0], resources.Sources)
	if err != nil || award.Gain == 0 {
		t.Fatalf("真实怪物无经验：%+v %v", award, err)
	}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	wantGrowth := uint64(116_914_950) + uint64(award.Gain)*3
	wantBrave := uint32(5_131_134) + award.Gain*3
	assertInfo := func(body []byte, growth uint64, claimExperience uint32) {
		t.Helper()
		raw := body[18 : 18+7424]
		// 独立字段位置：5260数量，5264经验余数，7408累计胶囊经验，7416实际领取经验。
		if binary.LittleEndian.Uint32(raw[5260:]) != 25 || binary.LittleEndian.Uint32(raw[5264:]) != wantBrave ||
			binary.LittleEndian.Uint64(raw[7408:]) != growth || binary.LittleEndian.Uint32(raw[7416:]) != claimExperience {
			t.Fatalf("商店数量、经验或领取经验与账号不一致：brave=%d/%d growth=%d claim_exp=%d", binary.LittleEndian.Uint32(raw[5260:]), binary.LittleEndian.Uint32(raw[5264:]), binary.LittleEndian.Uint64(raw[7408:]), binary.LittleEndian.Uint32(raw[7416:]))
		}
	}
	pushes := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 1339 {
			pushes++
			assertInfo(frame.Body, wantGrowth, 1_000_000)
		}
	}
	if pushes != 1 || wantGrowth%config.Capsule.MaximumExperience == 0 {
		t.Fatal("连续击杀未刷新第二个胶囊的非零进度")
	}
	// 同账号的68级角色重新打开页面，检查账号累计不依赖当前满级角色快照。
	c, _, err := repos.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	c.CharacterID, c.Level, c.Slot = "100", 68, 1
	c.Stats = map[string]int64{"exp": 0}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	skill, _, err := repos.Skill.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	skill.CharacterID, skill.Points.SyncedLevel = "100", 68
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	p.selectedCharacterID = 100
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentRequestAdventureInfo(p, []byte{100, 0}); err != nil {
		t.Fatal(err)
	}
	frames := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
	if len(frames) != 1 || frames[0].Header.MsgID != 1401 || frames[0].Body[0] != 1 {
		t.Fatal("未满级角色打开冒险团失败")
	}
	assertInfo(frames[0].Body[1:], wantGrowth, 1_000_000)
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentAdventureGrowthCapsule(p, nil); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "100")
	if c.Stats["exp"] != int64(config.Capsule.GrantedExperience) {
		t.Fatalf("未满级角色领取经验失败：%+v", c)
	}
	pushes = 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 1339 {
			pushes++
			assertInfo(frame.Body, wantGrowth-config.Capsule.MinimumExperience, 1_000_000)
		}
	}
	if pushes != 1 {
		t.Fatal("领取后未同步账号剩余经验及实际领取经验")
	}
	// 一次领取的页面经验必须与实际奖励一致；领完所有完整档位后显示0。
	for i := 0; i < 9; i++ {
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentAdventureGrowthCapsule(p, nil); err != nil {
			t.Fatal(err)
		}
	}
	remaining := wantGrowth - config.Capsule.MinimumExperience*10
	account, _, err := repos.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := adventuregroup.ParseRuntimeState(account, config, s.gameplayNow())
	if err != nil || loaded.GrowthExperience != remaining {
		t.Fatalf("领完十档余额错误：%+v %v", loaded, err)
	}
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 1339 {
			assertInfo(frame.Body, remaining, 0)
		}
	}
	if err := s.handleCurrentAdventureGrowthCapsule(p, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Account.Load(ctx, "account-1")
	if after.Metadata[adventuregroup.RuntimeStateMetadataKey] != account.Metadata[adventuregroup.RuntimeStateMetadataKey] {
		t.Fatal("不足一档时仍扣除了胶囊经验")
	}
	t.Logf("真实PVF：单格%d、十格总量%d；三怪累计%d，骁勇余数%d；68级角色按页面预告领取1,000,000经验，剩余累计扣除一档后仍可继续领取", config.Capsule.MaximumExperience, adventuregroup.CapsuleTotalCapacity(config.Capsule), wantGrowth, wantBrave)
}
