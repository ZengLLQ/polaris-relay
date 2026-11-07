package builder

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ZengLLQ/polaris-relay/pkg/state"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

// BlockBuilder produces profit-maximizing execution payloads from mempool and bundles.
type BlockBuilder struct {
	ID           string
	Address      types.Address
	Mempool      []*types.Transaction
	Bundles      []*types.Bundle
	StateDB      *state.StateDB
	BlockGasMax  uint64
	mu           sync.RWMutex
}

// NewBlockBuilder creates a new block builder instance.
func NewBlockBuilder(id string, addr types.Address, stateDB *state.StateDB, gasLimit uint64) *BlockBuilder {
	return &BlockBuilder{
		ID:          id,
		Address:     addr,
		Mempool:     make([]*types.Transaction, 0),
		Bundles:     make([]*types.Bundle, 0),
		StateDB:     stateDB,
		BlockGasMax: gasLimit,
	}
}

// SubmitBundle adds a searcher bundle to the builder's incoming queue.
func (b *BlockBuilder) SubmitBundle(bundle *types.Bundle) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Bundles = append(b.Bundles, bundle)
}

// SubmitTransaction adds a public mempool transaction.
func (b *BlockBuilder) SubmitTransaction(tx *types.Transaction) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Mempool = append(b.Mempool, tx)
}

// HasStateConflict determines if two bundles have conflicting state access sets.
func HasStateConflict(b1, b2 *types.Bundle) bool {
	writeSet1 := make(map[string]struct{})
	for _, tx := range b1.Transactions {
		for _, w := range tx.StateWrites {
			writeSet1[w] = struct{}{}
		}
	}

	for _, tx := range b2.Transactions {
		for _, r := range tx.StateReads {
			if _, exists := writeSet1[r]; exists {
				return true
			}
		}
		for _, w := range tx.StateWrites {
			if _, exists := writeSet1[w]; exists {
				return true
			}
		}
	}
	return false
}

// BuildOptimalBlock solves the combinatorial bundle selection and creates an ExecutionPayload.
func (b *BlockBuilder) BuildOptimalBlock(
	blockNumber uint64,
	parentHash types.Hash,
	feeRecipient types.Address,
	baseFee *big.Int,
	builderPayment *big.Int,
) (*types.ExecutionPayload, *big.Int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Working state snapshot
	snapshotID := b.StateDB.Snapshot()

	type BundleCandidate struct {
		Bundle      *types.Bundle
		GasUsed     uint64
		TotalReward *big.Int
		Score       *big.Float
	}

	candidates := make([]BundleCandidate, 0, len(b.Bundles))

	// 1. Evaluate candidate bundles speculatively
	for _, bundle := range b.Bundles {
		subSnap := b.StateDB.Snapshot()
		var bundleGas uint64
		bundleTip := big.NewInt(0)
		failed := false

		for _, tx := range bundle.Transactions {
			gas, tip, err := b.StateDB.ApplyTransaction(tx, baseFee, b.Address)
			if err != nil {
				// Check if this reverting tx is allowed
				isAllowedRevert := false
				for _, rHash := range bundle.RevertingTxHashes {
					if rHash == tx.Hash() {
						isAllowedRevert = true
						break
					}
				}
				if !isAllowedRevert {
					failed = true
					break
				}
			} else {
				bundleGas += gas
				bundleTip.Add(bundleTip, tip)
			}
		}

		b.StateDB.RevertToSnapshot(subSnap)

		if !failed && bundleGas > 0 {
			totalVal := new(big.Int).Add(bundleTip, bundle.BidAmount)
			score := new(big.Float).Quo(
				new(big.Float).SetInt(totalVal),
				new(big.Float).SetUint64(bundleGas),
			)
			candidates = append(candidates, BundleCandidate{
				Bundle:      bundle,
				GasUsed:     bundleGas,
				TotalReward: totalVal,
				Score:       score,
			})
		}
	}

	// Sort candidate bundles by score descending (greedy knapsack approximation)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score.Cmp(candidates[j].Score) > 0
	})

	var totalGasUsed uint64
	totalValueAccumulated := big.NewInt(0)
	includedTxs := make([]*types.Transaction, 0)
	acceptedBundles := make([]*types.Bundle, 0)

	// 2. Greedily commit non-conflicting bundles
	for _, cand := range candidates {
		if totalGasUsed+cand.GasUsed > b.BlockGasMax {
			continue
		}

		// Check conflict with previously accepted bundles
		conflict := false
		for _, accepted := range acceptedBundles {
			if HasStateConflict(cand.Bundle, accepted) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}

		// Apply transactions permanently to working state
		for _, tx := range cand.Bundle.Transactions {
			gas, tip, err := b.StateDB.ApplyTransaction(tx, baseFee, b.Address)
			if err == nil {
				totalGasUsed += gas
				totalValueAccumulated.Add(totalValueAccumulated, tip)
				includedTxs = append(includedTxs, tx)
			}
		}
		totalValueAccumulated.Add(totalValueAccumulated, cand.Bundle.BidAmount)
		acceptedBundles = append(acceptedBundles, cand.Bundle)
	}

	// 3. Fill remaining block space with mempool transactions
	sort.Slice(b.Mempool, func(i, j int) bool {
		return b.Mempool[i].GasPrice.Cmp(b.Mempool[j].GasPrice) > 0
	})

	for _, tx := range b.Mempool {
		if totalGasUsed+tx.GasLimit > b.BlockGasMax {
			continue
		}
		gas, tip, err := b.StateDB.ApplyTransaction(tx, baseFee, b.Address)
		if err == nil {
			totalGasUsed += gas
			totalValueAccumulated.Add(totalValueAccumulated, tip)
			includedTxs = append(includedTxs, tx)
		}
	}

	// 4. Inject Direct Builder-to-Proposer payment transaction if builderPayment > 0
	if builderPayment != nil && builderPayment.Sign() > 0 {
		builderAcc := b.StateDB.GetAccount(b.Address)
		data := []byte("builder_proposer_payment")
		paymentTx := types.NewTransaction(
			builderAcc.Nonce,
			b.Address,
			feeRecipient,
			builderPayment,
			state.CalculateIntrinsicGas(data),
			baseFee,
			data,
		)
		gas, _, err := b.StateDB.ApplyTransaction(paymentTx, baseFee, feeRecipient)
		if err == nil {
			totalGasUsed += gas
			includedTxs = append(includedTxs, paymentTx)
		}
	}

	stateRoot := b.StateDB.ComputeStateRoot()

	// Compute receipts root mock hash
	receiptsHasher := sha256.New()
	for _, tx := range includedTxs {
		h := tx.Hash()
		receiptsHasher.Write(h[:])
	}
	var receiptsRoot types.Hash
	copy(receiptsRoot[:], receiptsHasher.Sum(nil))

	payload := &types.ExecutionPayload{
		ParentHash:    parentHash,
		FeeRecipient:  feeRecipient,
		StateRoot:     stateRoot,
		ReceiptsRoot:  receiptsRoot,
		BlockNumber:   blockNumber,
		GasLimit:      b.BlockGasMax,
		GasUsed:       totalGasUsed,
		Timestamp:     uint64(time.Now().Unix()),
		ExtraData:     []byte(fmt.Sprintf("polaris-builder-%s", b.ID)),
		BaseFeePerGas: baseFee,
		Transactions:  includedTxs,
	}

	payload.ComputeBlockHash()

	// Clean working state back to initial unless committed
	_ = snapshotID

	return payload, totalValueAccumulated, nil
}
