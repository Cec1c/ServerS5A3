package worldmap

import "testing"

// 本文件钉住锦标赛/大会族在 PVF 侧的结构化解析。
// 夹具按真档 `dungeon/shonantournament/yellowdragon.dgn` +
// `map/tournament/yellowdragon_00.map` 的原文形状裁剪（见交付报告"PVF 取证"）。

func TestParseDungeonRequiredItemSectionIsStructured(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/shonantournament/yellowdragon.dgn", `
[maze info]
[size]
1 1
[start map]
0 0
[/start map]

[boss map]
0 0
[/boss map]

[tournament dungeon]
1
[required item]
4726 1 1
[/required item]

[limit party count]
1
`)
	dungeon := ParseDungeon(120, "dungeon/shonantournament/yellowdragon.dgn", doc)
	items := dungeon.Metadata.RequiredItems
	if len(items) != 1 {
		t.Fatalf("required items = %+v", items)
	}
	if items[0].ItemID != 4726 || items[0].Count != 1 || !items[0].ConsumeOnEntry {
		t.Fatalf("required item row = %+v, want {4726 1 true}", items[0])
	}
	for _, section := range dungeon.OpaqueSections {
		if sectionKey(section.Name) == "required item" {
			t.Fatalf("`[required item]` must no longer be opaque: %+v", section)
		}
	}
	if !dungeon.Metadata.Flags["tournament dungeon"] {
		t.Fatalf("`[tournament dungeon]` flag missing: %+v", dungeon.Metadata.Flags)
	}
}

// 厂商 DungeonFile.cs:938：只有第 3 个 token == 1 才是 ConsumeOnEntry；
// 938-940：ItemId <= 0 或 Count <= 0 的行丢弃。
func TestParseDungeonRequiredItemTriplesFollowVendorGrammar(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/required_items.dgn", `
[required item]
4726 2 0 4727 3 1 0 5 1 4728 0 1
[/required item]
`)
	dungeon := ParseDungeon(900, "dungeon/required_items.dgn", doc)
	items := dungeon.Metadata.RequiredItems
	if len(items) != 2 {
		t.Fatalf("required items = %+v", items)
	}
	if items[0].ItemID != 4726 || items[0].Count != 2 || items[0].ConsumeOnEntry {
		t.Fatalf("row 0 = %+v, want {4726 2 false}", items[0])
	}
	if items[1].ItemID != 4727 || items[1].Count != 3 || !items[1].ConsumeOnEntry {
		t.Fatalf("row 1 = %+v, want {4727 3 true}", items[1])
	}
}

// 厂商 DungeonFile.cs:934 的步进口径：不足三个的尾部余数被**静默忽略**，
// 而不是 malformed；同时该段不再进 opaque。
func TestParseDungeonRequiredItemTrailingRemainderIsIgnored(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/short_required_item.dgn", `
[required item]
4726 1
[/required item]

[required item]
4727 2 1
[/required item]
`)
	dungeon := ParseDungeon(901, "dungeon/short_required_item.dgn", doc)
	items := dungeon.Metadata.RequiredItems
	if len(items) != 1 {
		t.Fatalf("required items = %+v want exactly the complete triple", items)
	}
	if items[0].ItemID != 4727 || items[0].Count != 2 || !items[0].ConsumeOnEntry {
		t.Fatalf("required item = %+v", items[0])
	}
	for _, section := range dungeon.OpaqueSections {
		if sectionKey(section.Name) == "required item" {
			t.Fatalf("`[required item]` must not stay opaque: %+v", section)
		}
	}
}

// 真档 `[result card]` 内嵌 `[reward item rate]`（yellowdragon.dgn 原文）。
func TestParseDungeonResultCardAndNestedRewardItemRate(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/shonantournament/yellowdragon.dgn", `
[result card]

[reward item rate]
3329 60 1 3329 35 2 3329 5 3
[/reward item rate]
0 0 0 100 1 70 0 30 2 80 0 20 3 90 0 10 4 100 0 0 5 0 100 0
[/result card]

[tournament clear reward exp]
0 0 1 30000 2 50000 3 100000 4 200000
[/tournament clear reward exp]
`)
	dungeon := ParseDungeon(120, "dungeon/shonantournament/yellowdragon.dgn", doc)
	cards := dungeon.Metadata.TournamentResultCards
	if len(cards) != 6 {
		t.Fatalf("result cards = %+v", cards)
	}
	want := TournamentResultCard{ResultKey: 1, GoldWeight: 70, ItemWeight: 0, EmptyWeight: 30}
	if cards[1] != want {
		t.Fatalf("result card 1 = %+v want %+v", cards[1], want)
	}
	if cards[5] != (TournamentResultCard{ResultKey: 5, GoldWeight: 0, ItemWeight: 100, EmptyWeight: 0}) {
		t.Fatalf("result card 5 = %+v", cards[5])
	}
	rates := dungeon.Metadata.TournamentRewardItemRates
	if len(rates) != 3 {
		t.Fatalf("reward item rates = %+v", rates)
	}
	if rates[0] != (TournamentRewardItemRate{ItemID: 3329, Weight: 60, Count: 1}) {
		t.Fatalf("reward item rate 0 = %+v", rates[0])
	}
	experiences := dungeon.Metadata.TournamentClearExperiences
	if len(experiences) != 5 {
		t.Fatalf("clear reward exp = %+v", experiences)
	}
	if experiences[4] != (TournamentRewardExperience{CompletedRounds: 4, Experience: 200000}) {
		t.Fatalf("clear reward exp round 4 = %+v", experiences[4])
	}
	if dungeon.Metadata.TournamentResultCardMalformed ||
		dungeon.Metadata.TournamentRewardItemRateMalformed ||
		dungeon.Metadata.TournamentRewardExpMalformed {
		t.Fatalf("real-shaped tournament reward sections reported malformed: %+v", dungeon.Metadata)
	}
}

// 厂商 DungeonFile.cs:1108-1112：整数个数必须能被宽度整除，否则整段 malformed。
func TestParseDungeonTournamentRewardMalformedFlags(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/bad_tournament_rewards.dgn", `
[result card]
0 0 0
[/result card]

[reward item rate]
3329 60
[/reward item rate]

[tournament clear reward exp]
0 0 1 30000 2
[/tournament clear reward exp]
`)
	dungeon := ParseDungeon(902, "dungeon/bad_tournament_rewards.dgn", doc)
	if !dungeon.Metadata.TournamentResultCardMalformed {
		t.Fatal("odd token count in `[result card]` must set TournamentResultCardMalformed")
	}
	if !dungeon.Metadata.TournamentRewardItemRateMalformed {
		t.Fatal("odd token count in `[reward item rate]` must set TournamentRewardItemRateMalformed")
	}
	if !dungeon.Metadata.TournamentRewardExpMalformed {
		t.Fatal("odd token count in `[tournament clear reward exp]` must set TournamentRewardExpMalformed")
	}
}

// 厂商 DungeonFile.cs:1130-1141：CompletedRounds 重复 ⇒ malformed。
func TestParseDungeonTournamentRewardExpDuplicateRoundIsMalformed(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/dup_rounds.dgn", `
[tournament clear reward exp]
0 0 0 10
[/tournament clear reward exp]
`)
	dungeon := ParseDungeon(903, "dungeon/dup_rounds.dgn", doc)
	if !dungeon.Metadata.TournamentRewardExpMalformed {
		t.Fatalf("duplicate CompletedRounds must set malformed: %+v", dungeon.Metadata.TournamentClearExperiences)
	}
}

func TestParseMapTournamentEnemiesAndStartAreas(t *testing.T) {
	doc := parseTestDocument(t, "map/tournament/yellowdragon_00.map", `
[player number]
2 8
[dungeon]
120
[/dungeon]

[type]
`+"`[boss]`"+`
[tournament start area]
1 360 270 1
[tournament start area]
1 760 270 2
[tournament enemies]
1 `+"`[monster]`"+` 56048 900 `+"`痞子张海`"+` 56047 300 `+"`文峰`"+` 56461 900 `+"`刘俊`"+`
[/tournament enemies]

[NPC]
1000 `+"`[left]`"+` 0 0 0
[/NPC]

[map name]
`+"`PVP无名`"+`
`)
	parsed := ParseMap(1200, "map/tournament/yellowdragon_00.map", doc)
	if parsed.TournamentDefinitionMalformed {
		t.Fatalf("real-shaped tournament map reported malformed: %+v", parsed.Diagnostics)
	}
	areas := parsed.TournamentStartAreas
	if len(areas) != 2 {
		t.Fatalf("tournament start areas = %+v", areas)
	}
	if areas[0] != (TournamentStartAreaInfo{PartyCount: 1, X: 360, Y: 270, Direction: 1}) ||
		areas[1] != (TournamentStartAreaInfo{PartyCount: 1, X: 760, Y: 270, Direction: 2}) {
		t.Fatalf("tournament start areas = %+v", areas)
	}
	candidates := parsed.TournamentEnemyCandidates
	if len(candidates) != 3 {
		t.Fatalf("tournament enemies = %+v", candidates)
	}
	// 第 4 个 token 是 Strength（权重），**不是坐标**（MapFile.cs:925-948）。
	if candidates[0] != (TournamentEnemyInfo{PartyCount: 1, Code: 56048, Strength: 900, Name: "痞子张海"}) {
		t.Fatalf("candidate 0 = %+v", candidates[0])
	}
	if candidates[1] != (TournamentEnemyInfo{PartyCount: 1, Code: 56047, Strength: 300, Name: "文峰"}) {
		t.Fatalf("candidate 1 = %+v", candidates[1])
	}
	for _, section := range parsed.OpaqueSections {
		key := sectionKey(section.Name)
		if key == "tournament enemies" || key == "tournament start area" {
			t.Fatalf("tournament section stayed opaque: %+v", section)
		}
	}
}

// 厂商 MapFile.cs:908-923：第 2 个 token 必须是 `[monster]` 或 `[apc]`，
// 且 `(tokens.Count-2) % 3 == 0`。
func TestParseMapTournamentEnemiesRejectsMalformedRows(t *testing.T) {
	doc := parseTestDocument(t, "map/tournament/bad.map", `
[tournament enemies]
1 `+"`[monster]`"+` 56048 900
[/tournament enemies]
`)
	parsed := ParseMap(1201, "map/tournament/bad.map", doc)
	if !parsed.TournamentDefinitionMalformed {
		t.Fatal("malformed `[tournament enemies]` must set TournamentDefinitionMalformed")
	}
	if len(parsed.TournamentEnemyCandidates) != 0 {
		t.Fatalf("malformed tournament enemies produced candidates: %+v", parsed.TournamentEnemyCandidates)
	}
}

func TestParseMapTournamentEnemiesApcBranchIsRetained(t *testing.T) {
	doc := parseTestDocument(t, "map/tournament/blackdragon_00.map", `
[tournament enemies]
1 `+"`[apc]`"+` 20712 300 `+"`虔诚的费纳音`"+` 20706 300 `+"`村长哈克`"+` 11512 300 `+"`加西尔`"+`
[/tournament enemies]
`)
	parsed := ParseMap(1202, "map/tournament/blackdragon_00.map", doc)
	if parsed.TournamentDefinitionMalformed {
		t.Fatalf("apc branch reported malformed: %+v", parsed.Diagnostics)
	}
	if len(parsed.TournamentEnemyCandidates) != 3 {
		t.Fatalf("apc candidates = %+v", parsed.TournamentEnemyCandidates)
	}
	if !parsed.TournamentEnemyCandidates[0].IsApc {
		t.Fatalf("apc candidate lost its branch: %+v", parsed.TournamentEnemyCandidates[0])
	}
}

func TestParseMapTournamentStartAreaRequiresFourTokens(t *testing.T) {
	doc := parseTestDocument(t, "map/tournament/bad_area.map", `
[tournament start area]
1 360 270
`)
	parsed := ParseMap(1203, "map/tournament/bad_area.map", doc)
	if !parsed.TournamentDefinitionMalformed {
		t.Fatal("3-token `[tournament start area]` must set TournamentDefinitionMalformed")
	}
	if len(parsed.TournamentStartAreas) != 0 {
		t.Fatalf("malformed start area produced rows: %+v", parsed.TournamentStartAreas)
	}
}
