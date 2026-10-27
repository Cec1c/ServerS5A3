package quest

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

func validSecondAwakeningQuestionAnswer(d Definition, s repo.QuestState) bool {
	a, ok := questionAnswerFromState(s)
	if !ok || d.QuestionOptionCount <= 0 || int(a) >= d.QuestionOptionCount {
		return false
	}
	// Legacy normal finishes stored the explicit answer before v1 was introduced.
	// Missing optional metadata is compatible; contradictory metadata is not.
	if v, ok := s.Extra[questionAnswerMetadataKey]; ok && v != questionAnswerMetadataVersion {
		return false
	}
	if v, ok := s.Extra[questionOptionCountExtraKey]; ok && v != strconv.Itoa(d.QuestionOptionCount) {
		return false
	}
	return true
}
