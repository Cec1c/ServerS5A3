package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skinwarehouse"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

func TestSkinWarehouseSummaryShowsOnlyDatedCurrentOwnership(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.Local)
	character := dnfrepo.CharacterRecord{CharacterID: "7", Job: "0", Stats: map[string]int64{}}
	weapon, ok := weaponskin.FromSource(27000)
	if !ok {
		t.Fatal("缺少测试武器")
	}
	registeredAt := now.Add(-3500 * time.Millisecond)
	var err error
	character, err = weaponskin.Register(character, weapon, registeredAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := weaponskin.Register(character, weapon, now); !errors.Is(err, weaponskin.ErrAlreadyRegistered) {
		t.Fatalf("重复登记没有被拒绝：%v", err)
	}
	for font := uint16(1); font <= 6; font++ {
		character.Stats[inventory.DamageFontOwnershipStatKey(font)] = now.AddDate(0, 1, 0).Unix()
		skinwarehouse.RecordAcquisition(character.Stats, skinwarehouse.DamageFont, uint32(font), now.Add(-time.Duration(7-font)*time.Second))
	}
	character.Stats[inventory.DamageFontSelectedStatKey] = 4
	character.Stats[inventory.DamageFontOwnershipStatKey(20)] = 0 // 旧永久字体，没有获得记录。
	character.Stats[inventory.DamageFontOwnershipStatKey(21)] = now.Unix()
	skinwarehouse.RecordAcquisition(character.Stats, skinwarehouse.DamageFont, 21, now)
	skinwarehouse.RecordAcquisition(character.Stats, skinwarehouse.DamageFont, 22, now) // 没有对应拥有数据。

	available := func(weaponskin.Appearance) bool { return true }
	first := skinSummaryForCharacter(character, 1, now, available)
	if len(first.entries) != 5 || first.appliedFont != 4 || first.pages != 2 || first.page != 1 ||
		first.entries[0].ID != 6 || first.entries[1].ID != 5 || first.entries[2].ID != 4 ||
		first.entries[3].Kind != skinwarehouse.Weapon || first.entries[3].ID != weapon.ID ||
		first.entries[3].Millis != registeredAt.UnixMilli() || first.entries[4].ID != 3 {
		t.Fatalf("总览第一页不符：%+v", first)
	}
	second := skinSummaryForCharacter(character, 99, now, available)
	if second.page != 2 || len(second.entries) != 2 || second.entries[0].ID != 2 || second.entries[1].ID != 1 {
		t.Fatalf("超出末页的请求没有落到最后一页：%+v", second)
	}
	character.Job = "1" // 当前职业与这把武器不匹配。
	if got := skinSummaryForCharacter(character, 2, now, available); len(got.entries) != 1 || got.entries[0].ID != 1 {
		t.Fatalf("职业不符的武器进入总览：%+v", got)
	}
	other := skinSummaryForCharacter(dnfrepo.CharacterRecord{CharacterID: "8", Job: "0"}, 1, now, available)
	if other.pages != 1 || other.page != 1 || len(other.entries) != 0 {
		t.Fatalf("其他角色继承了获得记录：%+v", other)
	}

	var packet [20]byte
	copy(packet[:4], "WSO1")
	binary.LittleEndian.PutUint32(packet[4:8], 11)
	binary.LittleEndian.PutUint32(packet[8:12], 12)
	binary.LittleEndian.PutUint16(packet[12:14], 2)
	binary.LittleEndian.PutUint32(packet[16:20], weaponskin.CatalogChecksum)
	request, valid := decodeSkinSummaryRequest(packet[:])
	if !valid || request != (skinSummaryRequest{id: 11, epoch: 12, page: 2}) {
		t.Fatalf("总览请求无法解析：%+v", request)
	}
	body := encodeSkinSummary(request, 0, second)
	if string(body[:4]) != "WSO2" || len(body) != 104 || body[13] != 2 ||
		binary.LittleEndian.Uint16(body[14:16]) != 2 || binary.LittleEndian.Uint16(body[18:20]) != 4 ||
		body[24] != byte(skinwarehouse.DamageFont) || binary.LittleEndian.Uint32(body[28:32]) != 2 ||
		binary.LittleEndian.Uint32(body[32:36]) != 20261124 || binary.LittleEndian.Uint32(body[36:40]) != 20261224 ||
		body[56] != 0 {
		t.Fatalf("总览回包字段不符：%x", body)
	}
	packet[14] = 1
	if _, valid := decodeSkinSummaryRequest(packet[:]); valid {
		t.Fatal("接受了非零保留字段")
	}
}
