package mevshare_test

import (
	"math/big"
	"testing"

	"github.com/ZengLLQ/polaris-relay/pkg/mevshare"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func TestMEVSharePrivacyHintsAndBackrunValidation(t *testing.T) {
	service := mevshare.NewMEVShareService()

	userAddr, _ := types.HexToAddress("0x1111111111111111111111111111111111111111")
	dexAddr, _ := types.HexToAddress("0x2222222222222222222222222222222222222222")
	searcherAddr, _ := types.HexToAddress("0x3333333333333333333333333333333333333333")

	calldata := []byte{0xa9, 0x05, 0x9c, 0xbb, 0x00, 0x01} // transfer(...)
	userTx := types.NewTransaction(0, userAddr, dexAddr, big.NewInt(10), 100000, big.NewInt(50), calldata)

	// 1. Register private tx with HintContractAddress + HintCalldata
	flags := mevshare.HintContractAddress | mevshare.HintCalldata
	hint := service.RegisterPrivateTransaction(userTx, flags)

	if hint.TxHash != userTx.Hash() {
		t.Errorf("hint hash mismatch")
	}
	if hint.ContractAddress != dexAddr {
		t.Errorf("contract address should be leaked according to flag")
	}
	if len(hint.CalldataPrefix) != 4 {
		t.Errorf("calldata prefix should be 4 bytes, got %d", len(hint.CalldataPrefix))
	}

	// 2. Searcher creates legitimate backrun bundle: [userTx, searcherTx]
	searcherTx := types.NewTransaction(0, searcherAddr, dexAddr, big.NewInt(0), 100000, big.NewInt(60), nil)
	validBackrun := mevshare.CreateBackrunBundle("backrun-1", searcherAddr, userTx, searcherTx, big.NewInt(100), 1)

	err := service.ValidateBackrun(userTx.Hash(), validBackrun)
	if err != nil {
		t.Fatalf("legitimate backrun should pass validation: %v", err)
	}

	// 3. Malicious searcher attempts frontrunning: [searcherTx, userTx]
	maliciousFrontrun := &types.Bundle{
		ID:           "frontrun-attempt",
		Searcher:     searcherAddr,
		Transactions: []*types.Transaction{searcherTx, userTx},
	}
	errFrontrun := service.ValidateBackrun(userTx.Hash(), maliciousFrontrun)
	if errFrontrun != mevshare.ErrFrontrunningDetected {
		t.Errorf("expected ErrFrontrunningDetected, got %v", errFrontrun)
	}
}
