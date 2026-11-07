package types_test

import (
	"math/big"
	"testing"

	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func TestHashAndAddressConversions(t *testing.T) {
	hexHash := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	h, err := types.HexToHash(hexHash)
	if err != nil {
		t.Fatalf("unexpected error parsing hex hash: %v", err)
	}
	if h.String() != hexHash {
		t.Errorf("expected string %s, got %s", hexHash, h.String())
	}
	if h.IsZero() {
		t.Errorf("hash should not be zero")
	}

	hexAddr := "0x1234567890abcdef1234567890abcdef12345678"
	addr, err := types.HexToAddress(hexAddr)
	if err != nil {
		t.Fatalf("unexpected error parsing hex address: %v", err)
	}
	if addr.String() != hexAddr {
		t.Errorf("expected string %s, got %s", hexAddr, addr.String())
	}
}

func TestTransactionHashing(t *testing.T) {
	from, _ := types.HexToAddress("0x1111111111111111111111111111111111111111")
	to, _ := types.HexToAddress("0x2222222222222222222222222222222222222222")
	tx1 := types.NewTransaction(0, from, to, big.NewInt(100), 21000, big.NewInt(10), []byte("hello"))
	tx2 := types.NewTransaction(0, from, to, big.NewInt(100), 21000, big.NewInt(10), []byte("hello"))
	tx3 := types.NewTransaction(1, from, to, big.NewInt(100), 21000, big.NewInt(10), []byte("hello"))

	if tx1.Hash() != tx2.Hash() {
		t.Errorf("identical transactions must produce identical hashes")
	}
	if tx1.Hash() == tx3.Hash() {
		t.Errorf("transactions with different nonces must have distinct hashes")
	}
}

func TestExecutionPayloadHeaderDerivation(t *testing.T) {
	from, _ := types.HexToAddress("0x1111111111111111111111111111111111111111")
	to, _ := types.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := types.NewTransaction(0, from, to, big.NewInt(100), 21000, big.NewInt(10), nil)

	payload := &types.ExecutionPayload{
		ParentHash:    types.Hash{1},
		FeeRecipient:  to,
		StateRoot:     types.Hash{2},
		ReceiptsRoot:  types.Hash{3},
		BlockNumber:   100,
		GasLimit:      30000000,
		GasUsed:       21000,
		Timestamp:     1700000000,
		ExtraData:     []byte("test"),
		BaseFeePerGas: big.NewInt(1000000000),
		Transactions:  []*types.Transaction{tx},
	}

	blockHash := payload.ComputeBlockHash()
	if blockHash.IsZero() {
		t.Errorf("block hash should not be zero")
	}

	header := payload.ToHeader()
	if header.BlockHash != blockHash {
		t.Errorf("blinded header block hash must match payload block hash")
	}
	if header.TransactionsRoot != payload.ComputeTransactionsRoot() {
		t.Errorf("blinded header transactions root must match payload tx root")
	}
}
