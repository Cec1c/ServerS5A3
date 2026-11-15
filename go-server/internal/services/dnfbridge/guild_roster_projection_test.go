package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildRosterProjectionPreservesWireBodies(t *testing.T) {
	s, peers, repos := guildTextFixture(t)
	membership, found, err := guild.ReadGuildMembership(context.Background(), repos.Settings, "1")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	owner := r.GuildOwner{CharacterID: "1", AccountID: s.accountIDForSession(peers[0])}
	full, err := membership.Display(owner)
	if err != nil || full == nil {
		t.Fatal(full, err)
	}
	projected, err := s.currentGuildForSelectedCharacter(peers[0])
	if err != nil || projected == nil {
		t.Fatal(projected, err)
	}
	now := s.gameplayNow()
	online := map[string]byte{"1": 11, "2": 23}
	for _, identity := range []bool{true, false} {
		for _, cursor := range []bool{true, false} {
			a := buildCurrentGuildMemberListBody(full, identity, cursor, online, now, 1)
			b := buildCurrentGuildMemberListBody(projected, identity, cursor, online, now, 1)
			if !bytes.Equal(a, b) {
				t.Fatal("roster body changed", identity, cursor)
			}
		}
		if !bytes.Equal(buildCurrentGuildGradeBody(full, identity), buildCurrentGuildGradeBody(projected, identity)) {
			t.Fatal("grade body changed")
		}
	}
	for grade := byte(0); grade <= 6; grade++ {
		if !bytes.Equal(buildCurrentGuildManagementBody(full, len(online), now, grade), buildCurrentGuildManagementBody(projected, len(online), now, grade)) {
			t.Fatal("management body changed", grade)
		}
	}
	if !bytes.Equal(buildCurrentGuildInfoBody(1, full), buildCurrentGuildInfoBody(1, projected)) || !bytes.Equal(buildCurrentGuildMembershipNotificationBody(full), buildCurrentGuildMembershipNotificationBody(projected)) {
		t.Fatal("identity body changed")
	}
	a, err := buildCurrentGuildHongbaoPointsBody(guild.SortedMembers(full))
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildCurrentGuildHongbaoPointsBody(guild.SortedMembers(projected))
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("contribution body changed", err)
	}
}
