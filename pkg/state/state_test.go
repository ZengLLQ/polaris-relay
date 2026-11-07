package state_test

import (
	"math/big"
	"testing"

	"github.com/ZengLLQ/polaris-relay/pkg/state"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func TestStateTransitionsAndJournalRollback(t *testing.T) {
	s := state.NewStateDB()
	addr1, _ := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addr2, _ := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	coinbase, _ := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")

	initialBal := new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18)) // 10 ETH
	s.SetAccount(&state.Account{
		Address: addr1,
		Nonce:   0,
		Balance: new(big.Int).Set(initialBal),
		Storage: make(map[string][]byte),
	})

	baseFee := big.NewInt(100)
	tx := types.NewTransaction(0, addr1, addr2, big.NewInt(1000), 21000, big.NewInt(150), nil)

	snapshot := s.Snapshot()

	// Apply transaction
	gasUsed, tip, err := s.ApplyTransaction(tx, baseFee, coinbase)
	if err != nil {
		t.Fatalf("failed to apply transaction: %v", err)
	}
	if gasUsed != 21000 {
		t.Errorf("expected gas used 21000, got %d", gasUsed)
	}
	// Tip should be (150 - 100) * 21000 = 1,050,000
	expectedTip := new(big.Int).Mul(big.NewInt(50), big.NewInt(21000))
	if tip.Cmp(expectedTip) != 0 {
		t.Errorf("expected tip %s, got %s", expectedTip, tip)
	}

	acc2 := s.GetAccount(addr2)
	if acc2.Balance.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("recipient balance expected 1000, got %s", acc2.Balance)
	}

	// Revert to snapshot
	s.RevertToSnapshot(snapshot)
	acc1Reverted := s.GetAccount(addr1)
	if acc1Reverted.Balance.Cmp(initialBal) != 0 {
		t.Errorf("expected reverted balance %s, got %s", initialBal, acc1Reverted.Balance)
	}
	acc2Reverted := s.GetAccount(addr2)
	if acc2Reverted.Balance.Sign() != 0 {
		t.Errorf("recipient balance should be zero after rollback, got %s", acc2Reverted.Balance)
	}
}

func TestEIP1559BaseFeeCalculation(t *testing.T) {
	parentBaseFee := big.NewInt(1000000000) // 1 Gwei
	gasLimit := uint64(30000000)
	targetGas := gasLimit / 2 // 15M

	// 1. Exactly at target -> base fee unchanged
	nextFeeTarget := state.CalculateNextBaseFee(parentBaseFee, targetGas, gasLimit)
	if nextFeeTarget.Cmp(parentBaseFee) != 0 {
		t.Errorf("base fee should not change when gasUsed == target")
	}

	// 2. Full block (30M gasUsed) -> base fee increases by 12.5% (1/8)
	nextFeeFull := state.CalculateNextBaseFee(parentBaseFee, gasLimit, gasLimit)
	expectedIncrease := new(big.Int).Div(parentBaseFee, big.NewInt(8))
	expectedNextFee := new(big.Int).Add(parentBaseFee, expectedIncrease)
	if nextFeeFull.Cmp(expectedNextFee) != 0 {
		t.Errorf("expected base fee %s, got %s", expectedNextFee, nextFeeFull)
	}

	// 3. Empty block (0 gasUsed) -> base fee decreases by 12.5%
	nextFeeEmpty := state.CalculateNextBaseFee(parentBaseFee, 0, gasLimit)
	expectedDecrease := new(big.Int).Div(parentBaseFee, big.NewInt(8))
	expectedNextEmpty := new(big.Int).Sub(parentBaseFee, expectedDecrease)
	if nextFeeEmpty.Cmp(expectedNextEmpty) != 0 {
		t.Errorf("expected base fee %s, got %s", expectedNextEmpty, nextFeeEmpty)
	}
}
