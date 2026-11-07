package state

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

var (
	ErrInsufficientBalance = errors.New("insufficient account balance")
	ErrNonceMismatch       = errors.New("nonce does not match expected account nonce")
	ErrGasLimitExceeded    = errors.New("transaction gas limit exceeds block gas remaining")
	ErrIntrinsicGas        = errors.New("intrinsic gas higher than transaction gas limit")
)

const (
	IntrinsicTxGas    uint64 = 21000
	GasPerZeroByte    uint64 = 4
	GasPerNonZeroByte uint64 = 16
	ElasticityFactor  uint64 = 2
	BaseFeeMaxChange  uint64 = 8
)

// Account represents account state.
type Account struct {
	Address types.Address
	Nonce   uint64
	Balance *big.Int
	Storage map[string][]byte
}

func (a *Account) Clone() *Account {
	st := make(map[string][]byte, len(a.Storage))
	for k, v := range a.Storage {
		valCopy := make([]byte, len(v))
		copy(valCopy, v)
		st[k] = valCopy
	}
	bal := new(big.Int)
	if a.Balance != nil {
		bal.Set(a.Balance)
	}
	return &Account{
		Address: a.Address,
		Nonce:   a.Nonce,
		Balance: bal,
		Storage: st,
	}
}

// JournalEntry stores an atomic mutation for rollback.
type JournalEntry struct {
	Address types.Address
	PrevAcc *Account
}

// StateDB manages atomic account state transitions.
type StateDB struct {
	accounts map[types.Address]*Account
	journal  []JournalEntry
	mu       sync.RWMutex
}

// NewStateDB creates a new in-memory state database.
func NewStateDB() *StateDB {
	return &StateDB{
		accounts: make(map[types.Address]*Account),
		journal:  make([]JournalEntry, 0),
	}
}

// GetAccount returns a copy of the account or creates a zero account if absent.
func (s *StateDB) GetAccount(addr types.Address) *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if acc, exists := s.accounts[addr]; exists {
		return acc.Clone()
	}
	return &Account{
		Address: addr,
		Nonce:   0,
		Balance: big.NewInt(0),
		Storage: make(map[string][]byte),
	}
}

// SetAccount updates or initializes an account.
func (s *StateDB) SetAccount(acc *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, exists := s.accounts[acc.Address]; exists {
		s.journal = append(s.journal, JournalEntry{Address: acc.Address, PrevAcc: prev.Clone()})
	} else {
		s.journal = append(s.journal, JournalEntry{Address: acc.Address, PrevAcc: nil})
	}
	s.accounts[acc.Address] = acc.Clone()
}

// Snapshot returns the current journal depth.
func (s *StateDB) Snapshot() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.journal)
}

// RevertToSnapshot rolls back state mutations made since snapshot id.
func (s *StateDB) RevertToSnapshot(snapshotID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.journal) - 1; i >= snapshotID; i-- {
		entry := s.journal[i]
		if entry.PrevAcc == nil {
			delete(s.accounts, entry.Address)
		} else {
			s.accounts[entry.Address] = entry.PrevAcc
		}
	}
	s.journal = s.journal[:snapshotID]
}

// ComputeStateRoot calculates a deterministic Merkle-style root over all accounts.
func (s *StateDB) ComputeStateRoot() types.Hash {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addresses := make([]string, 0, len(s.accounts))
	for addr := range s.accounts {
		addresses = append(addresses, addr.String())
	}
	sort.Strings(addresses)

	hasher := sha256.New()
	var buf [8]byte
	for _, addrStr := range addresses {
		addr, _ := types.HexToAddress(addrStr)
		acc := s.accounts[addr]
		hasher.Write(acc.Address.Bytes())
		binary.BigEndian.PutUint64(buf[:], acc.Nonce)
		hasher.Write(buf[:])
		if acc.Balance != nil {
			hasher.Write(acc.Balance.Bytes())
		}
	}
	var out types.Hash
	copy(out[:], hasher.Sum(nil))
	return out
}

// CalculateIntrinsicGas returns standard gas cost of transaction data.
func CalculateIntrinsicGas(data []byte) uint64 {
	gas := IntrinsicTxGas
	for _, b := range data {
		if b == 0 {
			gas += GasPerZeroByte
		} else {
			gas += GasPerNonZeroByte
		}
	}
	return gas
}

// ApplyTransaction executes a single transaction against the state.
func (s *StateDB) ApplyTransaction(
	tx *types.Transaction,
	baseFee *big.Int,
	coinbase types.Address,
) (gasUsed uint64, tipFee *big.Int, err error) {
	intrinsic := CalculateIntrinsicGas(tx.Data)
	if tx.GasLimit < intrinsic {
		return 0, nil, fmt.Errorf("%w: limit %d < intrinsic %d", ErrIntrinsicGas, tx.GasLimit, intrinsic)
	}

	fromAcc := s.GetAccount(tx.From)
	if tx.Nonce != fromAcc.Nonce {
		return 0, nil, fmt.Errorf("%w: got %d, expected %d", ErrNonceMismatch, tx.Nonce, fromAcc.Nonce)
	}

	// Calculate total gas cost: GasLimit * GasPrice
	maxGasFee := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), tx.GasPrice)
	totalCost := new(big.Int).Add(maxGasFee, tx.Value)

	if fromAcc.Balance.Cmp(totalCost) < 0 {
		return 0, nil, fmt.Errorf("%w: balance %s < cost %s", ErrInsufficientBalance, fromAcc.Balance, totalCost)
	}

	// Deduct balance and increment nonce
	fromAcc.Balance.Sub(fromAcc.Balance, totalCost)
	fromAcc.Nonce++
	s.SetAccount(fromAcc)

	// Credit value to recipient
	toAcc := s.GetAccount(tx.To)
	toAcc.Balance.Add(toAcc.Balance, tx.Value)
	s.SetAccount(toAcc)

	gasUsed = intrinsic

	// Calculate priority tip: GasPrice - BaseFee
	tipPerGas := new(big.Int).Sub(tx.GasPrice, baseFee)
	if tipPerGas.Sign() < 0 {
		tipPerGas.SetInt64(0)
	}
	tipFee = new(big.Int).Mul(tipPerGas, new(big.Int).SetUint64(gasUsed))

	// Credit priority fee to coinbase
	coinbaseAcc := s.GetAccount(coinbase)
	coinbaseAcc.Balance.Add(coinbaseAcc.Balance, tipFee)
	s.SetAccount(coinbaseAcc)

	// Refund unused gas: (GasLimit - gasUsed) * GasPrice
	unusedGas := tx.GasLimit - gasUsed
	refund := new(big.Int).Mul(new(big.Int).SetUint64(unusedGas), tx.GasPrice)
	fromAcc = s.GetAccount(tx.From)
	fromAcc.Balance.Add(fromAcc.Balance, refund)
	s.SetAccount(fromAcc)

	return gasUsed, tipFee, nil
}

// CalculateNextBaseFee implements the EIP-1559 base fee adjustment algorithm.
func CalculateNextBaseFee(parentBaseFee *big.Int, parentGasUsed, parentGasLimit uint64) *big.Int {
	gasTarget := parentGasLimit / ElasticityFactor
	if parentGasUsed == gasTarget {
		return new(big.Int).Set(parentBaseFee)
	}

	if parentGasUsed > gasTarget {
		gasDelta := parentGasUsed - gasTarget
		num := new(big.Int).Mul(parentBaseFee, new(big.Int).SetUint64(gasDelta))
		denom := new(big.Int).Mul(new(big.Int).SetUint64(gasTarget), new(big.Int).SetUint64(BaseFeeMaxChange))
		feeDelta := new(big.Int).Div(num, denom)
		if feeDelta.Sign() == 0 {
			feeDelta.SetInt64(1)
		}
		return new(big.Int).Add(parentBaseFee, feeDelta)
	}

	// parentGasUsed < gasTarget
	gasDelta := gasTarget - parentGasUsed
	num := new(big.Int).Mul(parentBaseFee, new(big.Int).SetUint64(gasDelta))
	denom := new(big.Int).Mul(new(big.Int).SetUint64(gasTarget), new(big.Int).SetUint64(BaseFeeMaxChange))
	feeDelta := new(big.Int).Div(num, denom)
	nextFee := new(big.Int).Sub(parentBaseFee, feeDelta)
	if nextFee.Sign() <= 0 {
		nextFee.SetInt64(1)
	}
	return nextFee
}
