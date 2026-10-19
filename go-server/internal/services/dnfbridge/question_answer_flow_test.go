package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestQuestionAnswerOp33ToOp34CompletesForEveryValidOption(t *testing.T) {
	for _, test := range []struct {
		quest  uint16
		answer byte
	}{
		{3868, 0}, {3868, 1}, {3868, 2}, {3869, 0}, {3869, 1},
	} {
		t.Run(fmt.Sprintf("quest-%d-answer-%d", test.quest, test.answer), func(t *testing.T) {
			ctx := context.Background()
			repositories := finishBridgeSeed(t)
			if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
				CharacterID: "19",
				States: map[int64]dnfrepo.QuestState{
					int64(test.quest): {Status: "active", ProgressValue: 1},
				},
			}); err != nil {
				t.Fatal(err)
			}
			catalog := questionAnswerTestCatalog(t)
			connection := &bufferConn{}
			service := &Service{
				options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
				questCatalog:       catalog,
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{
				conn: connection, connID: fmt.Sprintf("question-%d-answer-%d", test.quest, test.answer),
				channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 19,
			}
			triggerBody := []byte{0x21, 0x00, 0, 0, 0x00, test.answer}
			binary.LittleEndian.PutUint16(triggerBody[2:4], test.quest)
			if err := service.handleCurrentSetQuestTrigger(session, triggerBody); err != nil {
				t.Fatal(err)
			}
			stored, found, err := repositories.Quest.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load answered quest found=%t err=%v", found, err)
			}
			state := stored.States[int64(test.quest)]
			if state.ProgressValue != 0 || state.Extra["question_answer_id"] != fmt.Sprint(test.answer) {
				t.Fatalf("persisted answer=%d state=%+v", test.answer, state)
			}

			finish := dnfquest.FinishQuestRequest{
				QuestID:           test.quest,
				RewardSelectIndex: ^uint16(0),
				Multiplier:        1,
				Reserved:          dnfquest.CurrentFinishQuestObservedTailMarker,
			}
			if err := service.handleCurrentFinishQuestWithResources(
				session,
				finish,
				repositories,
				catalog,
				finishBridgeProgression(t),
				finishBridgeAllocator(nil),
			); err != nil && !errors.Is(err, dnfquest.ErrFinishQuestNotPending) {
				t.Fatal(err)
			}
			completed, found, err := repositories.Quest.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load completed quest found=%t err=%v", found, err)
			}
			state = completed.States[int64(test.quest)]
			if state.Status != "completed" || state.Extra["question_answer_id"] != fmt.Sprint(test.answer) {
				t.Fatalf("completed answer=%d state=%+v packets=%x", test.answer, state, connection.write.Bytes())
			}
		})
	}
}

func TestQuestionAnswerReplayKeyDistinguishesRawAnswersAndConflictingChoiceDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	repositories := finishBridgeSeed(t)
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3868: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		questCatalog:       questionAnswerTestCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn: connection, connID: "question-answer-conflict",
		channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 19,
	}
	body := func(answer byte) []byte {
		return []byte{0x21, 0x00, 0x1c, 0x0f, 0x00, answer}
	}
	if err := service.handleCurrentSetQuestTrigger(session, body(1)); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	if err := service.handleCurrentSetQuestTrigger(session, body(2)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || failure.Header.MsgID != 0x21 || failure.Header.Classification != 1 ||
		len(failure.Body) != 2 || failure.Body[1] != 22 {
		t.Fatalf("conflicting answer packet=%+v body=%x trailing=%x", failure.Header, failure.Body, trailing)
	}
	stored, _, _ := repositories.Quest.Load(ctx, "19")
	if state := stored.States[3868]; state.Extra["question_answer_id"] != "1" {
		t.Fatalf("conflicting answer overwrote persisted choice: %+v", state)
	}
}

func TestQuestionAnswerSendFailureRetriesWithoutDuplicatingState(t *testing.T) {
	ctx := context.Background()
	repositories := finishBridgeSeed(t)
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3868: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("answer ACK write failed")
	connection := &failNthDungeonWriteConn{failAt: 1, err: writeErr}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		questCatalog:       questionAnswerTestCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn: connection, connID: "question-answer-send-retry",
		channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 19,
	}
	body := []byte{0x21, 0x00, 0x1c, 0x0f, 0x00, 0x01}
	if err := service.handleCurrentSetQuestTrigger(session, body); !errors.Is(err, writeErr) {
		t.Fatalf("first send error=%v want=%v", err, writeErr)
	}
	session.conn = &connection.bufferConn
	if err := service.handleCurrentSetQuestTrigger(session, body); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := repositories.Quest.Load(ctx, "19")
	if state := stored.States[3868]; state.ProgressValue != 0 || state.Extra["question_answer_id"] != "1" {
		t.Fatalf("retry state=%+v", state)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != 0x21 || binary.LittleEndian.Uint32(ack.Body[3:7]) != 0 {
		t.Fatalf("retry ACK=%+v body=%x trailing=%x", ack.Header, ack.Body, trailing)
	}
}

func TestQuestionFinishWithoutAnswerSendsFailureAndDoesNotArchive(t *testing.T) {
	ctx := context.Background()
	repositories := finishBridgeSeed(t)
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3868: {Status: "active", ProgressValue: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := questionAnswerTestCatalog(t)
	connection := &bufferConn{}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn: connection, connID: "question-answerless-finish",
		channel: channelcatalog.Channel{ID: 19}, selectedCharacterID: 19,
	}
	finish := dnfquest.FinishQuestRequest{
		QuestID:           3868,
		RewardSelectIndex: ^uint16(0),
		Multiplier:        1,
		Reserved:          dnfquest.CurrentFinishQuestObservedTailMarker,
	}
	if err := service.handleCurrentFinishQuestWithResources(
		session,
		finish,
		repositories,
		catalog,
		finishBridgeProgression(t),
		finishBridgeAllocator(nil),
	); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || failure.Header.MsgID != 0x22 || failure.Header.Classification != 1 ||
		len(failure.Body) != 2 || failure.Body[1] != 22 {
		t.Fatalf("answerless finish packet=%+v body=%x trailing=%x", failure.Header, failure.Body, trailing)
	}
	stored, _, _ := repositories.Quest.Load(ctx, "19")
	if state := stored.States[3868]; state.Status != "active" {
		t.Fatalf("answerless finish archived question: %+v", state)
	}
}

type questionAnswerPVFSource map[string]string

func (s questionAnswerPVFSource) ReadText(path string) (string, error) {
	text, ok := s[path]
	if !ok {
		return "", fmt.Errorf("missing test PVF path %s", path)
	}
	return text, nil
}

func questionAnswerTestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := questionAnswerPVFSource{
		dnfquest.DefaultList: "3868 `answer3.qst`\n3869 `answer2.qst`\n",
		"n_quest/answer3.qst": "[grade]\n`[epic]`\n" +
			"[difficulty]\n`N`\n" +
			"[level]\n1 99\n" +
			"[job]\n`[all]`\n" +
			"[exposed by npc]\n1\n" +
			"[type]\n`[question]`\n" +
			"[reward type]\n`[item]`\n" +
			"[reward int data]\n0 0\n" +
			"[question info]\n`Prompt one` `Prompt two` `Option zero` `Option one` `Option two`\n",
		"n_quest/answer2.qst": "[grade]\n`[epic]`\n" +
			"[difficulty]\n`N`\n" +
			"[level]\n1 99\n" +
			"[job]\n`[all]`\n" +
			"[exposed by npc]\n1\n" +
			"[type]\n`[question]`\n" +
			"[reward type]\n`[item]`\n" +
			"[reward int data]\n0 0\n" +
			"[question info]\n`Prompt one` `Prompt two` `Option zero` `Option one`\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
