package mevshare

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"sync"

	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

var (
	ErrInvalidBackrun       = errors.New("backrun bundle does not contain target transaction")
	ErrFrontrunningDetected = errors.New("frontrunning detected: searcher tx placed before target tx")
)

// PrivacyFlags controls which transaction fields are leaked to searchers.
type PrivacyFlags uint8

const (
	HintHash            PrivacyFlags = 1 << 0
	HintContractAddress PrivacyFlags = 1 << 1
	HintCalldata        PrivacyFlags = 1 << 2
	HintLogs            PrivacyFlags = 1 << 3
)

// TransactionHint represents a sanitized, privacy-masked transaction event.
type TransactionHint struct {
	TxHash          types.Hash    `json:"tx_hash"`
	ContractAddress types.Address `json:"contract_address,omitempty"`
	CalldataPrefix  []byte        `json:"calldata_prefix,omitempty"`
	GasLimit        uint64        `json:"gas_limit"`
	GasPrice        *big.Int      `json:"gas_price"`
}

// MEVShareService manages private order flow and backrun auctions.
type MEVShareService struct {
	hints   map[types.Hash]*TransactionHint
	targets map[types.Hash]*types.Transaction
	mu      sync.RWMutex
}

// NewMEVShareService creates a new MEV-share service.
func NewMEVShareService() *MEVShareService {
	return &MEVShareService{
		hints:   make(map[types.Hash]*TransactionHint),
		targets: make(map[types.Hash]*types.Transaction),
	}
}

// RegisterPrivateTransaction creates a redacted hint for searchers according to privacy flags.
func (s *MEVShareService) RegisterPrivateTransaction(tx *types.Transaction, flags PrivacyFlags) *TransactionHint {
	s.mu.Lock()
	defer s.mu.Unlock()

	h := tx.Hash()
	s.targets[h] = tx

	hint := &TransactionHint{
		TxHash:   h,
		GasLimit: tx.GasLimit,
		GasPrice: new(big.Int).Set(tx.GasPrice),
	}

	if flags&HintContractAddress != 0 {
		hint.ContractAddress = tx.To
	}
	if flags&HintCalldata != 0 && len(tx.Data) >= 4 {
		hint.CalldataPrefix = append([]byte(nil), tx.Data[:4]...) // 4-byte function selector
	}

	s.hints[h] = hint
	return hint
}

// ValidateBackrun verifies that a searcher's bundle strictly backruns the private transaction
// without inserting any transaction in front of it.
func (s *MEVShareService) ValidateBackrun(targetHash types.Hash, backrunBundle *types.Bundle) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetTx, exists := s.targets[targetHash]
	if !exists {
		return ErrInvalidBackrun
	}

	if len(backrunBundle.Transactions) < 2 {
		return ErrInvalidBackrun
	}

	// First transaction must be targetTx
	firstTxHash := backrunBundle.Transactions[0].Hash()
	if firstTxHash != targetHash {
		// Check if target is later in bundle (which would indicate frontrunning)
		for _, tx := range backrunBundle.Transactions {
			if tx.Hash() == targetHash {
				return ErrFrontrunningDetected
			}
		}
		return ErrInvalidBackrun
	}

	// Verify target tx matches registered content
	if backrunBundle.Transactions[0].Nonce != targetTx.Nonce ||
		backrunBundle.Transactions[0].From != targetTx.From {
		return ErrInvalidBackrun
	}

	return nil
}

// CreateBackrunBundle constructs an atomic bundle containing [targetTx, searcherTx].
func CreateBackrunBundle(
	bundleID string,
	searcher types.Address,
	targetTx *types.Transaction,
	searcherTx *types.Transaction,
	bid *big.Int,
	targetBlock uint64,
) *types.Bundle {
	return &types.Bundle{
		ID:                bundleID,
		Searcher:          searcher,
		Transactions:      []*types.Transaction{targetTx, searcherTx},
		TargetBlock:       targetBlock,
		MinTimestamp:      0,
		MaxTimestamp:      ^uint64(0),
		RevertingTxHashes: nil,
		BidAmount:         new(big.Int).Set(bid),
	}
}

// ComputeHintSignature returns a tamper-evident digest of a hint.
func (h *TransactionHint) ComputeHintSignature() types.Hash {
	hasher := sha256.New()
	hasher.Write(h.TxHash[:])
	hasher.Write(h.ContractAddress[:])
	hasher.Write(h.CalldataPrefix)
	var out types.Hash
	copy(out[:], hasher.Sum(nil))
	return out
}
