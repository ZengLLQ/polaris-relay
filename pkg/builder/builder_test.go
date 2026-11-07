package builder_test

import (
	"math/big"
	"testing"

	"github.com/ZengLLQ/polaris-relay/pkg/builder"
	"github.com/ZengLLQ/polaris-relay/pkg/state"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func TestBundleConflictDetection(t *testing.T) {
	addr1, _ := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2, _ := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// Bundle 1 writes to pool_A
	tx1 := types.NewTransaction(0, addr1, addr2, big.NewInt(0), 50000, big.NewInt(10), nil)
	tx1.StateWrites = []string{"uniswap_v3_pool_A"}
	b1 := &types.Bundle{ID: "b1", Transactions: []*types.Transaction{tx1}}

	// Bundle 2 reads pool_A
	tx2 := types.NewTransaction(0, addr2, addr1, big.NewInt(0), 50000, big.NewInt(10), nil)
	tx2.StateReads = []string{"uniswap_v3_pool_A"}
	b2 := &types.Bundle{ID: "b2", Transactions: []*types.Transaction{tx2}}

	// Bundle 3 writes to pool_B (no conflict with b1)
	tx3 := types.NewTransaction(1, addr1, addr2, big.NewInt(0), 50000, big.NewInt(10), nil)
	tx3.StateWrites = []string{"uniswap_v3_pool_B"}
	b3 := &types.Bundle{ID: "b3", Transactions: []*types.Transaction{tx3}}

	if !builder.HasStateConflict(b1, b2) {
		t.Errorf("expected conflict between b1 and b2 on pool_A")
	}
	if builder.HasStateConflict(b1, b3) {
		t.Errorf("expected no conflict between b1 and b3")
	}
}

func TestBuildOptimalBlock(t *testing.T) {
	stateDB := state.NewStateDB()
	builderAddr, _ := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	proposerAddr, _ := types.HexToAddress("0xpppppppppppppppppppppppppppppppppppppppp")
	searcher1, _ := types.HexToAddress("0x1111111111111111111111111111111111111111")

	// Fund accounts
	stateDB.SetAccount(&state.Account{
		Address: builderAddr,
		Balance: new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18)),
	})
	stateDB.SetAccount(&state.Account{
		Address: searcher1,
		Balance: new(big.Int).Mul(big.NewInt(50), big.NewInt(1e18)),
	})

	b := builder.NewBlockBuilder("builder-alpha", builderAddr, stateDB, 30000000)

	// Submit searcher bundle
	bundleTx := types.NewTransaction(0, searcher1, builderAddr, big.NewInt(1000), 21000, big.NewInt(200), nil)
	bundle := &types.Bundle{
		ID:           "bundle-arbitrage",
		Searcher:     searcher1,
		Transactions: []*types.Transaction{bundleTx},
		BidAmount:    new(big.Int).Mul(big.NewInt(2), big.NewInt(1e17)), // 0.2 ETH direct payment
	}
	b.SubmitBundle(bundle)

	builderBid := new(big.Int).Mul(big.NewInt(15), big.NewInt(1e16)) // 0.15 ETH to proposer
	baseFee := big.NewInt(100)
	payload, totalReward, err := b.BuildOptimalBlock(
		1,
		types.Hash{0xaa},
		proposerAddr,
		baseFee,
		builderBid,
	)

	if err != nil {
		t.Fatalf("failed to build optimal block: %v", err)
	}

	if payload.BlockNumber != 1 {
		t.Errorf("expected block number 1, got %d", payload.BlockNumber)
	}
	if len(payload.Transactions) != 2 { // 1 bundle tx + 1 direct builder payment tx
		t.Fatalf("expected 2 transactions, got %d", len(payload.Transactions))
	}
	if totalReward.Sign() <= 0 {
		t.Errorf("total reward must be positive, got %s", totalReward)
	}

	// Verify payment transaction details
	paymentTx := payload.Transactions[1]
	if paymentTx.From != builderAddr || paymentTx.To != proposerAddr {
		t.Errorf("payment transaction routing mismatch")
	}
	if paymentTx.Value.Cmp(builderBid) != 0 {
		t.Errorf("payment value mismatch: expected %s, got %s", builderBid, paymentTx.Value)
	}
}
