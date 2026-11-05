package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type duplicateTownReadCharacterRepository struct {
	dnfrepo.CharacterRepository
	loads int
}

func (r *duplicateTownReadCharacterRepository) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.loads++
	return r.CharacterRepository.Load(ctx, id)
}

// An already published route is an acknowledgement boundary. No new actor,
// location write, equipment initialization, or account projection is needed.
func TestInitialTownCompletedProgressRetryDoesNotReadRepositories(t *testing.T) {
	for _, stage := range []currentInitialTownRouteStage{currentInitialTownRouteTransitionSent, currentInitialTownRoutePlayerStateSent} {
		for _, acknowledged := range []bool{false, true} {
			service, session, repositories := newTownMoveTest(t)
			connection := session.conn.(*bufferConn)
			counter := &duplicateTownReadCharacterRepository{CharacterRepository: repositories.Character}
			repositories.Character = counter
			reads := 0
			service.repositoryProvider = func() (dnfrepo.Group, bool) { reads++; return repositories, true }
			session.initialTownRouteCharacterID = session.selectedCharacterID
			session.initialTownRouteStage = stage
			for retry := 0; retry < 3; retry++ {
				connection.write.Reset()
				if err := service.sendCurrentInitialTownRoute(session, currentInitialTownProgress, acknowledged, false); err != nil {
					t.Fatal(err)
				}
				if reads != 0 || counter.loads != 0 {
					t.Fatalf("completed route stage=%d acknowledge=%t read provider=%d character=%d", stage, acknowledged, reads, counter.loads)
				}
				if acknowledged {
					ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
					if ack.Header.MsgID != uint16(dnfenum.CmdPacketChangeTutorialFlag) || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 0}) || len(rest) != 0 {
						t.Fatalf("duplicate ack changed: header=%+v body=%x rest=%x", ack.Header, ack.Body, rest)
					}
				} else if connection.write.Len() != 0 {
					t.Fatalf("server resume invented progress ACK: %x", connection.write.Bytes())
				}
			}
		}
	}
}

func TestInitialTownCompletedReturnRetryConsumesPendingWithoutReads(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	session.returnSelectTownReentryPending = true
	reads := 0
	service.repositoryProvider = func() (dnfrepo.Group, bool) { reads++; return dnfrepo.Group{}, false }
	if err := service.resumeCurrentInitialTownRouteAfterReturnSelect(session); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || session.returnSelectTownReentryPending || session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("completed return retry reads=%d pending=%t", reads, session.returnSelectTownReentryPending)
	}
}

func TestInitialTownDuplicateKeepsOwnershipAndPartialRetryBoundaries(t *testing.T) {
	for _, test := range []struct {
		name          string
		configure     func(*gameSession)
		partial       bool
		progress      uint32
		requireReturn bool
	}{
		{name: "different_character", configure: func(p *gameSession) { p.initialTownRouteCharacterID++ }},
		{name: "no_selected_character", configure: func(p *gameSession) { p.selectedCharacterID = 0 }},
		{name: "not_armed", configure: func(p *gameSession) { p.initialTownRouteStage = currentInitialTownRouteIdle }},
		{name: "wrong_progress", configure: func(p *gameSession) {}, progress: currentInitialTownProgress + 1},
		{name: "unowned_return_select", configure: func(p *gameSession) {}, requireReturn: true},
		{name: "active_dungeon", configure: func(p *gameSession) { p.dungeon.runtime = &runtimeDungeonState{} }},
		{name: "partial_route", configure: func(p *gameSession) { p.initialTownRouteStage = currentInitialTownRouteActorBound }, partial: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			session.initialTownRouteCharacterID = session.selectedCharacterID
			session.initialTownRouteStage = currentInitialTownRouteTransitionSent
			test.configure(session)
			reads := 0
			service.repositoryProvider = func() (dnfrepo.Group, bool) { reads++; return repositories, true }
			progress := test.progress
			if progress == 0 {
				progress = currentInitialTownProgress
			}
			if err := service.sendCurrentInitialTownRoute(session, progress, true, test.requireReturn); err != nil {
				t.Fatal(err)
			}
			if test.partial {
				if reads == 0 {
					t.Fatal("partially published route skipped authoritative data reads")
				}
			} else if reads != 0 || session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatalf("unowned route read=%d bytes=%x", reads, session.conn.(*bufferConn).write.Bytes())
			}
		})
	}
}

type duplicateTownAckFailureConn struct{ bufferConn }

func (*duplicateTownAckFailureConn) Write([]byte) (int, error) {
	return 0, errors.New("duplicate town ACK write failure")
}

func TestInitialTownCompletedProgressRetryPropagatesWriteFailureWithoutReads(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	session.conn = &duplicateTownAckFailureConn{}
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	reads := 0
	service.repositoryProvider = func() (dnfrepo.Group, bool) { reads++; return dnfrepo.Group{}, false }
	if err := service.handleCurrentInitialTownProgress(session, currentInitialTownProgress); err == nil {
		t.Fatal("duplicate progress ACK swallowed write failure")
	}
	if reads != 0 || session.initialTownRouteStage != currentInitialTownRouteTransitionSent {
		t.Fatalf("failed duplicate ACK reads=%d stage=%d", reads, session.initialTownRouteStage)
	}
}
