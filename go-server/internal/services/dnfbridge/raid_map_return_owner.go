package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// Internal town-only owner frozen at legal normal-Anton entry. It is not a
// global clear journal, reward receipt, protocol field or retry authority.
type currentAntonMapReturnOwner struct {
	Session        *gameSession
	DungeonSession *worldmap.DungeonSession
	CharacterID    uint16
	Generation     uint64
	AccountID      string
	DungeonID      int64
	Entry          currentRaidDungeonEntry
	Completed      bool
}

func freezeCurrentAntonMapReturnOwner(session *gameSession, r *runtimeDungeonState) {
	if session == nil || r == nil || r.Session == nil || r.raidEntry == nil || r.antonMapReturnOwner != nil {
		return
	}
	snapshot := r.Session.Snapshot()
	if snapshot.Run.Status != worldmap.DungeonRunActive || snapshot.Run.DungeonID != r.Dungeon.ID {
		return
	}
	e := *r.raidEntry
	e.Participants = append([]uint16(nil), e.Participants...)
	r.antonMapReturnOwner = &currentAntonMapReturnOwner{Session: session, DungeonSession: r.Session, CharacterID: session.selectedCharacterID, Generation: session.characterGeneration, AccountID: r.AccountID, DungeonID: r.Dungeon.ID, Entry: e}
}

func currentAntonMapReturnOwnerMatches(session *gameSession, r *runtimeDungeonState) bool {
	if session == nil || r == nil || r.Session == nil || r.raidEntry == nil || r.antonMapReturnOwner == nil {
		return false
	}
	o, e := r.antonMapReturnOwner, r.raidEntry
	f := &o.Entry
	if o.Session != session || o.DungeonSession != r.Session || o.CharacterID != session.selectedCharacterID || o.Generation != session.characterGeneration || o.AccountID != r.AccountID || o.DungeonID != r.Dungeon.ID || r.Session.Snapshot().Run.DungeonID != r.Dungeon.ID ||
		f.RaidKey != e.RaidKey || f.InstanceID != e.InstanceID || f.RunID == "" || f.RunID != e.RunID || f.Phase != e.Phase || f.Attempt != e.Attempt || f.ChannelID != e.ChannelID || f.ChannelType != e.ChannelType || !sameRaidParticipants(f.Participants, e.Participants) {
		return false
	}
	for _, id := range f.Participants {
		if id == o.CharacterID {
			return true
		}
	}
	return false
}

// Called by the real completed producer after authenticated local completion,
// before checking whether global progress still accepts this map's clear.
// No journal, asset, member clear count or phase state is modified here.
func recordCurrentAntonMapReturnCompletion(b *runtimeRaidBattle, session *gameSession, r *runtimeDungeonState) {
	if b == nil || b.Mode != 1 || b.Single || !currentAntonMapReturnOwnerMatches(session, r) {
		return
	}
	e := r.raidEntry
	if b.InstanceID != e.InstanceID || b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType || b.MemberAccounts[session.selectedCharacterID] != r.AccountID || r.AccountID != session.accountID || r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		return
	}
	r.antonMapReturnOwner.Completed = true
}
