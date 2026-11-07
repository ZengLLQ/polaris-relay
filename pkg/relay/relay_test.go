package relay_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/ZengLLQ/polaris-relay/pkg/relay"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func TestEndToEndPBSAuctionAndUnblinding(t *testing.T) {
	rel := relay.NewPolarisRelay(50 * time.Millisecond)

	builderAddr, _ := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	proposerAddr, _ := types.HexToAddress("0xpppppppppppppppppppppppppppppppppppppppp")
	parentHash := types.Hash{0xde, 0xad, 0xbe, 0xef}
	slot := uint64(100)

	// 1. Register builder with 5 ETH collateral
	collateral := new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18))
	err := rel.RegisterBuilder("builder-01", "pubkey-01", builderAddr, collateral)
	if err != nil {
		t.Fatalf("failed to register builder: %v", err)
	}

	// 2. Builder prepares block with 0.5 ETH bid
	bidValue := new(big.Int).Mul(big.NewInt(5), big.NewInt(1e17)) // 0.5 ETH
	paymentTx := types.NewTransaction(0, builderAddr, proposerAddr, bidValue, 21000, big.NewInt(100), nil)

	payload := &types.ExecutionPayload{
		ParentHash:    parentHash,
		FeeRecipient:  proposerAddr,
		StateRoot:     types.Hash{0x01},
		ReceiptsRoot:  types.Hash{0x02},
		BlockNumber:   100,
		GasLimit:      30000000,
		GasUsed:       21000,
		Timestamp:     uint64(time.Now().Unix()),
		BaseFeePerGas: big.NewInt(100),
		Transactions:  []*types.Transaction{paymentTx},
	}
	payload.ComputeBlockHash()

	// 3. Submit block to relay
	builderBid, err := rel.SubmitBlock("builder-01", slot, payload, bidValue, []byte("builder_sig"))
	if err != nil {
		t.Fatalf("failed to submit block: %v", err)
	}
	if builderBid.Value.Cmp(bidValue) != 0 {
		t.Errorf("expected bid %s, got %s", bidValue, builderBid.Value)
	}

	// 4. Proposer requests highest bid header
	header, winningVal, err := rel.GetHeader(slot, parentHash)
	if err != nil {
		t.Fatalf("failed to get header: %v", err)
	}
	if winningVal.Cmp(bidValue) != 0 {
		t.Errorf("expected winning value %s, got %s", bidValue, winningVal)
	}
	if header.BlockHash != payload.BlockHash {
		t.Errorf("header block hash must match submitted payload hash")
	}

	// 5. Proposer signs blinded header and submits to relay
	proposerPubkey := "validator_pubkey_77"
	sig := relay.ComputeMockValidatorSignature("proposer_privkey", header.BlockHash, slot)
	signedBlock := &types.SignedBlindedBeaconBlock{
		Header:         header,
		Slot:           slot,
		ProposerPubkey: proposerPubkey,
		Signature:      sig,
	}

	unblindedPayload, err := rel.SubmitBlindedBlock(signedBlock)
	if err != nil {
		t.Fatalf("failed to submit blinded block: %v", err)
	}
	if unblindedPayload.BlockHash != payload.BlockHash {
		t.Errorf("unblinded payload hash mismatch")
	}
	if len(unblindedPayload.Transactions) != 1 {
		t.Errorf("expected 1 transaction in unblinded payload, got %d", len(unblindedPayload.Transactions))
	}
}

func TestEquivocationSlashingDetection(t *testing.T) {
	rel := relay.NewPolarisRelay(50 * time.Millisecond)

	builderAddr, _ := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	proposerAddr, _ := types.HexToAddress("0xpppppppppppppppppppppppppppppppppppppppp")
	slot := uint64(200)

	rel.RegisterBuilder("builder-01", "pk1", builderAddr, big.NewInt(2e18))

	// Create payload 1
	p1 := &types.ExecutionPayload{
		ParentHash:   types.Hash{0x01},
		FeeRecipient: proposerAddr,
		BlockNumber:  200,
		Transactions: []*types.Transaction{},
	}
	p1.ComputeBlockHash()
	rel.SubmitBlock("builder-01", slot, p1, big.NewInt(0), nil)

	// Create payload 2 (different hash)
	p2 := &types.ExecutionPayload{
		ParentHash:   types.Hash{0x02},
		FeeRecipient: proposerAddr,
		BlockNumber:  200,
		Transactions: []*types.Transaction{},
	}
	p2.ComputeBlockHash()

	proposerPubkey := "validator_pubkey_99"

	// Sign block 1
	signedBlock1 := &types.SignedBlindedBeaconBlock{
		Header:         p1.ToHeader(),
		Slot:           slot,
		ProposerPubkey: proposerPubkey,
		Signature:      []byte("sig_block_1"),
	}
	_, err1 := rel.SubmitBlindedBlock(signedBlock1)
	if err1 != nil {
		t.Fatalf("first signed block should succeed: %v", err1)
	}

	// Maliciously sign conflicting block 2 for the same slot
	signedBlock2 := &types.SignedBlindedBeaconBlock{
		Header:         p2.ToHeader(),
		Slot:           slot,
		ProposerPubkey: proposerPubkey,
		Signature:      []byte("sig_block_2"),
	}
	_, err2 := rel.SubmitBlindedBlock(signedBlock2)
	if err2 != relay.ErrEquivocationDetected {
		t.Errorf("expected ErrEquivocationDetected, got %v", err2)
	}

	proofs := rel.GetEquivocationProofs()
	if len(proofs) != 1 {
		t.Fatalf("expected 1 equivocation proof, got %d", len(proofs))
	}
	if proofs[0].Slot != slot || proofs[0].ProposerPubkey != proposerPubkey {
		t.Errorf("equivocation proof metadata mismatch")
	}
}
