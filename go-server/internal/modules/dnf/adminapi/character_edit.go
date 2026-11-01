package adminapi

const CharacterEditProtocol = "character-admission-v1"

type CharacterEditStatus struct {
	Protocol    string `json:"protocol"`
	AccountID   string `json:"accountId"`
	CharacterID uint16 `json:"characterId"`
	Busy        bool   `json:"busy"`
}
