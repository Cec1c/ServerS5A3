package fiendwar

// 超时空之战：战况表格子上要画的"怪物头像"用的是**真实的怪物 ID**，而这只怪是
// 谁，写在 PVF 里，不是服务端自编的一张表：
//
//	dungeon/gcontents/180621_fiendwar/cosmo_*.dgn
//	  [maze info] → [map specification] `boss` x y <mapID>
//	map/gcontents/180621_fiendwar/dungeon/(2,0)_Boss_<mapID>.map
//	  [monster] <怪物ID> ... `[fixed]` `[boss]`
//
// 本文件把这条对应关系固化成表；`raid_fiendwar_monster_pvf_test.go` 会逐条回读
// 真 PVF 复核（每张 maze 的 BOSS 房地图、里面那只 [boss]、以及 maze 张数），
// 数据漂移会让测试先红，而不是等玩家看见错的头像。
//
// 权威出处（百度百科「超时空之战」· 怪物信息）：
// "其中前哨基地/兵站本营/攻击基地中的怪物以随机分配的形式出现在地图中。"
// —— 分配发生在**建图那一刻**，所以前哨基地/兵站本营的领主是开团时就抽定的
// （见 FiendWarBattle.FixtureMazes，按格子位置钉死），雷达通关后战况表显示的与进图实际打到的才会
// 是同一只。
//
// 权威出处（同一页 · 地图构造）："雷达：该副本通关后增加所有玩家10%技能伤害，
// 并显示地图中怪物头像。（反之怪物头像显示问号）" —— 揭示的开关就是 RADAR STATE。

// fiendWarArmyBossIDs 是"攻击基地用"的四只稀有怪，也就是敌军军团 type 1..4 会走进
// 的那张 maze（maze = type-1）的领主：
//
//	1 魅妖公主(110979) / 2 融合型恶魔(110977) / 3 狂热恶魔:破坏者(110987) / 4 超合金恶魔:艾罗伊(110982)
//
// 千分比权重就是 PVF 的 [ENEMY CREATE RATE] 1 30000 2 30000 3 30000 4 10000。
var fiendWarArmyBossIDs = [4]uint32{110979, 110977, 110987, 110982}

// fiendWarOrcBossIDs 是"随机中立"的原生怪（非移动型）领主，也就是 monster type
// 1..2 会走进的那张 maze（maze = type+3）的领主：
//
//	1 面霸1号(110714) / 2 兽人队长:血之埃克斯(110975)
//
// maze 4/5 的小怪是山贼兽人/山贼弩兵/山贼头头（FarmerOrc/ArcherOrc/BanditOrc/BanditOrcHead），
// 与百科"原生怪物（非移动型怪物）· 随机中立"那一栏一致。
var fiendWarOrcBossIDs = [2]uint32{110714, 110975}

// FiendWarFixtureBossIDs 是每个设施副本、逐张 maze 的领主。
//
// 长度就是这张副本的 maze 张数：雷达/炮塔/量子时空传送站只有 1 张（领主恒定），
// 前哨基地与兵站本营各有 3 张（领主随机分配，开团抽定）。
var FiendWarFixtureBossIDs = map[int][]uint32{
	// 雷达基地：磁力恶魔:马格内特
	4963: {110973},
	// 炮塔：常春魔藤
	4965: {110980},
	// 前哨基地：人类背叛者:宾柏 / 引力恶魔:薛丁恶 / 宇宙粉碎机:克鲁瑟
	4967: {110981, 110984, 110708},
	4969: {110981, 110984, 110708},
	4971: {110981, 110984, 110708},
	// 兵站本营：光之恶魔:欧格里 / 暗之恶魔:艾森 / 重力恶魔:白斯
	4975: {110709, 110978, 110992},
	4976: {110709, 110978, 110992},
	4977: {110709, 110978, 110992},
	// 量子时空传送站：宇宙恶魔:罗什（纯白 / 黑暗）
	4953: {110995},
	4978: {110994},
}

// FiendWarArmyBossID 返回敌军军团某兵种对应的领主。
func FiendWarArmyBossID(enemyType byte) (uint32, bool) {
	if enemyType < 1 || int(enemyType) > len(fiendWarArmyBossIDs) {
		return 0, false
	}
	return fiendWarArmyBossIDs[enemyType-1], true
}

// FiendWarOrcBossID 返回原生怪（中立兽人）某兵种对应的领主。
func FiendWarOrcBossID(monsterType byte) (uint32, bool) {
	if monsterType < 1 || int(monsterType) > len(fiendWarOrcBossIDs) {
		return 0, false
	}
	return fiendWarOrcBossIDs[monsterType-1], true
}

// FiendWarFixtureBossID 返回设施副本第 maze 张图的领主。
func FiendWarFixtureBossID(dungeon, maze int) (uint32, bool) {
	bosses, ok := FiendWarFixtureBossIDs[dungeon]
	if !ok || maze < 0 || maze >= len(bosses) {
		return 0, false
	}
	return bosses[maze], true
}
