package main

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestFilterAbsoluteCubeTokensDropsOnlyGatedJobBlocks(t *testing.T) {
	tokens := []platformpvf.ScriptToken{
		{Type: 3, Text: "[absolute bind cube]"}, {Type: 0, Value: 1},
		{Type: 3, Text: "[absolute bind plus rare]"},
		{Type: 6, Text: "[swordman]"},
		{Type: 6, Text: "hair"}, {Type: 0, Value: 101560001},
		{Type: 6, Text: "hat"}, {Type: 0, Value: 101550001},
		{Type: 6, Text: "face"}, {Type: 0, Value: 101570001},
		{Type: 6, Text: "neck"}, {Type: 0, Value: 101520001},
		{Type: 6, Text: "coat"}, {Type: 0, Value: 101500001},
		{Type: 6, Text: "belt"}, {Type: 0, Value: 101530001},
		{Type: 6, Text: "pants"}, {Type: 0, Value: 101510001},
		{Type: 6, Text: "shoes"}, {Type: 0, Value: 101540001},
		{Type: 6, Text: "[knight]"},
		{Type: 6, Text: "hair"}, {Type: 0, Value: 101560002},
		{Type: 6, Text: "hat"}, {Type: 0, Value: 101550002},
		{Type: 6, Text: "face"}, {Type: 0, Value: 101570002},
		{Type: 6, Text: "neck"}, {Type: 0, Value: 101520002},
		{Type: 6, Text: "coat"}, {Type: 0, Value: 101500002},
		{Type: 6, Text: "belt"}, {Type: 0, Value: 101530002},
		{Type: 6, Text: "pants"}, {Type: 0, Value: 101510002},
		{Type: 6, Text: "shoes"}, {Type: 0, Value: 101540002},
		{Type: 3, Text: "[/absolute bind plus rare]"},
		{Type: 3, Text: "[/absolute bind cube]"},
	}
	got, err := filterAbsoluteCubeTokens(tokens, map[int]map[string]bool{1: map[string]bool{"[swordman]": true}})
	if err != nil {
		t.Fatal(err)
	}
	jobCount := 0
	knightCount := 0
	for _, token := range got {
		if token.Type == 6 && token.Text == "[swordman]" {
			jobCount++
		}
		if token.Type == 6 && token.Text == "[knight]" {
			knightCount++
		}
	}
	if jobCount != 1 || knightCount != 0 {
		t.Fatalf("filtered jobs swordman=%d knight=%d", jobCount, knightCount)
	}
}

func TestGatedCompleteSameColorFamilyRemainsCandidate(t *testing.T) {
	makeFamily := func(base int64) completeFamily {
		ids := map[int64]bool{}
		parts := map[string]int64{}
		items := map[string]rareAvatar{}
		for index, part := range avatarPartOrder {
			id := base + int64(index)
			ids[id] = true
			parts[part] = id
			items[part] = rareAvatar{ID: id, Part: part}
		}
		return completeFamily{
			Job: "[knight]", Stem: "光明星佑绮晶", ColorTok: "光明", Color: 0,
			IDs: ids, PartIDs: parts, Items: items,
		}
	}
	gated := makeFamily(1000)
	other := makeFamily(2000)
	families := []completeFamily{gated, other}
	claimed := map[string]int{}
	claimCubeFamily("[knight]", map[string]bool{"[swordman]": true}, gated.PartIDs["coat"], "光明", 0, families, claimed, 1)
	if len(claimed) != 0 {
		t.Fatalf("gated mapping claimed complete families: %v", claimed)
	}
	remaining := unclaimedFamilies(families, "光明", 0, claimed)
	if len(remaining) != 2 {
		t.Fatalf("gated complete same-color family was hidden: remaining=%d", len(remaining))
	}
	claimCubeFamily("[knight]", map[string]bool{"[knight]": true}, gated.PartIDs["coat"], "光明", 0, families, claimed, 1)
	remaining = unclaimedFamilies(families, "光明", 0, claimed)
	if len(remaining) != 1 || remaining[0].Items["coat"].ID != other.Items["coat"].ID {
		t.Fatalf("usable claim did not leave the other complete family as candidate: %+v", remaining)
	}
}

func TestRealSkyCubeAuditAfter(t *testing.T) {
	pvfPath := os.Getenv("SKY_AUDIT_PVF")
	expect := os.Getenv("SKY_AUDIT_EXPECT")
	if pvfPath == "" || expect == "" {
		t.Skip("set SKY_AUDIT_PVF and SKY_AUDIT_EXPECT=after")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	report, _, err := auditArchive(archive, pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cubes) != 14 {
		t.Fatalf("cubes=%d want=14", len(report.Cubes))
	}
	if len(report.Aliases.Aliases) != 17 || len(report.Aliases.JobsByKey[0]) == 0 {
		t.Fatalf("alias audit incomplete: aliases=%d key0jobs=%v", len(report.Aliases.Aliases), report.Aliases.JobsByKey[0])
	}
	if containsFold(report.Aliases.JobsByKey[0], "[knight]") {
		t.Fatal("key0 unexpectedly has an external knight alias")
	}
	if !report.OuterBox.OptionsExact || !report.OuterBox.AttachFree || !report.OuterBox.AuctionAllowed || len(report.OuterBox.Errors) != 0 {
		t.Fatalf("outer box contract failed: %+v", report.OuterBox)
	}
	if len(report.MissingSupportedJobs) != 0 || len(report.GenuineOmissionCandidates) != 0 {
		t.Fatalf("supported/candidate mismatch: missing=%v candidates=%v", report.MissingSupportedJobs, report.GenuineOmissionCandidates)
	}
	for _, cube := range report.Cubes {
		for _, job := range cube.UsableJobs {
			if !containsFold(cube.MappedJobs, job) {
				t.Fatalf("cube %d usable job %s has no complete output block", cube.ID, job)
			}
		}
		for _, evidence := range cube.CoatEvidence {
			if evidence.Unclaimed {
				t.Fatalf("cube %d has an unclaimed same-color job coat: %+v", cube.ID, evidence)
			}
		}
	}
	if expect != "after" {
		t.Fatalf("unknown SKY_AUDIT_EXPECT=%q", expect)
	}
	if !report.Passed || len(report.RemovedBlocks) != 0 {
		t.Fatalf("after PVF not clean: passed=%t blocks=%d errors=%v", report.Passed, len(report.RemovedBlocks), report.Errors)
	}
}
func TestEnsureDistinctPathsRejectsReportAlias(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "candidate.pvf"
	if err := ensureDistinctPaths(map[string]string{"input": path, "report": path}); err == nil {
		t.Fatal("expected input/report alias rejection")
	}
}
