package repository

import "context"

// GuildRankingCharacter is a read-only fact set, never a saveable character.
type GuildRankingCharacter struct {
	CharacterID string
	AccountID   string
	Slot        int
	Level       int
	DeleteFlag  int64
	PVPGrade    int64
	RaidClears  map[string]int64
}

type GuildRankingSnapshot struct {
	Accounts map[string][]GuildRankingCharacter
	Members  map[string]GuildRankingCharacter
}

type GuildRankingSnapshotReader interface {
	LoadGuildRankingSnapshot(context.Context, []string, []string, int) (GuildRankingSnapshot, error)
}
