package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The live tower entry spent more than three seconds writing its already-built
// containers. Subsequent repository reads must still load the authoritative
// actor and quest state before op120 commits placement.
type postStartMapSlowContainerConn struct {
	bufferConn
	delayed bool
}

func (c *postStartMapSlowContainerConn) Write(p []byte) (int, error) {
	if !c.delayed && len(p) >= 13 && p[0] == 0 && binary.LittleEndian.Uint16(p[1:3]) == 13 {
		c.delayed = true
		time.Sleep(createWriteTimeout + 100*time.Millisecond)
	}
	return c.bufferConn.Write(p)
}

type postStartMapDeadlineQuestRepository struct {
	dnfrepo.QuestRepository
	loadsAfterContainers int
	containersWritten    *bool
}

func (r *postStartMapDeadlineQuestRepository) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return dnfrepo.QuestRecord{}, false, err
	}
	if *r.containersWritten {
		r.loadsAfterContainers++
	}
	return r.QuestRepository.Load(ctx, id)
}

func TestPostStartMapSlowContainerWriteKeepsAuthoritativePlacement(t *testing.T) {
	service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true})
	service.characterStats = testCharacterStatTable(t)
	connection := &postStartMapSlowContainerConn{}
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	quest := &postStartMapDeadlineQuestRepository{QuestRepository: repositories.Quest, containersWritten: &connection.delayed}
	repositories.Quest = quest
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	channel := channelcatalog.Channel{ServerID: 1, ID: 11, Type: 1, Port: 10011}
	session := &gameSession{conn: connection, channel: channel, residentChannel: channel, selectedCharacterID: 99}
	if err := service.sendCurrentPostStartMapPlayerPlacement(session, runtime, runtime.Session.Snapshot().Scene, "slow_container_regression"); err != nil {
		t.Fatalf("placement failed after a successful slow container write: %v", err)
	}
	if !connection.delayed || quest.loadsAfterContainers == 0 {
		t.Fatalf("fixture missed the live boundary: delayed=%t quest_reads=%d", connection.delayed, quest.loadsAfterContainers)
	}
	rest := assertCurrentPostStartMapPlayerState(t, session, connection.write.Bytes(), true, false)
	if len(rest) != 0 || !session.postStartMapPlayerStateSent {
		t.Fatal("complete quest/actor/placement sequence was not committed")
	}
}
