package repository

import "context"

// DisjointerAssetUnitOfWork includes the requester's account ledger and shared
// materials, both characters' wallets and inventories, and the machine state.
// The owner may equal the requester. Implementations lock the account before
// characters so concurrent epic-piece credits cannot be lost.
type DisjointerAssetUnitOfWork interface {
	WithinDisjointerAssets(context.Context, string, string, string,
		func(AccountRepository, CharacterRepository, InventoryRepository, AccountInventoryRepository) error) error
}
