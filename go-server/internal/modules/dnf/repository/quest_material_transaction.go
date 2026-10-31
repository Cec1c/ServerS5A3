package repository

import (
	"context"
	"strings"
)

// QuestMaterialPreparationUnitOfWork atomically freezes personal decisions for
// all present recipients before an authoritative combat death is committed.
// Only Quest and the recipients' material Settings namespaces are exposed.
// No session queue or character/inventory lock may be acquired in this callback.
type QuestMaterialPreparationUnitOfWork interface {
	WithinQuestMaterialPreparation(context.Context, []string, func(Group) error) error
}

func QuestMaterialSettingsScope(characterID, scope string) bool {
	prefix := "character:" + characterID + ":quest_material:"
	return characterID != "" && strings.HasPrefix(scope, prefix) && len(scope) > len(prefix)
}
