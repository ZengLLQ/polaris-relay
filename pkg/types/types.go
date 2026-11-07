package types

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

// Hash represents a 32-byte cryptographic hash.
type Hash [32]byte

func (h Hash) String() string {
	return "0x" + hex.EncodeToString(h[:])
}

func (h Hash) Bytes() []byte {
	return h[:]
}

func (h Hash) IsZero() bool {
	return h == Hash{}
}

// HexToHash converts a hex string into a Hash.
func HexToHash(s string) (Hash, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s) != 64 {
		return Hash{}, fmt.Errorf("invalid hash length: expected 64 hex characters, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return Hash{}, err
	}
	var h Hash
	copy(h[:], b)
	return h, nil
}

// Address represents a 20-byte account address.
type Address [20]byte

func (a Address) String() string {
	return "0x" + hex.EncodeToString(a[:])
}

func (a Address) Bytes() []byte {
	return a[:]
}

func (a Address) IsZero() bool {
	return a == Address{}
}

// HexToAddress converts a hex string into an Address.
func HexToAddress(s string) (Address, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s) != 40 {
		return Address{}, fmt.Errorf("invalid address length: expected 40 hex characters, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return Address{}, err
	}
	var a Address
	copy(a[:], b)
	return a, nil
}

// Transaction represents an atomic state transition message.
type Transaction struct {
	Nonce       uint64   `json:"nonce"`
	From        Address  `json:"from"`
	To          Address  `json:"to"`
	Value       *big.Int `json:"value"`
	GasLimit    uint64   `json:"gas_limit"`
	GasPrice    *big.Int `json:"gas_price"`
	Data        []byte   `json:"data"`
	StateReads  []string `json:"state_reads"`  // Keys read during execution
	StateWrites []string `json:"state_writes"` // Keys modified during execution
	Signature   []byte   `json:"signature"`
	hash        Hash
	once        sync.Once
}

// NewTransaction creates a new Transaction with initialized big.Ints.
func NewTransaction(
	nonce uint64,
	from Address,
	to Address,
	value *big.Int,
	gasLimit uint64,
	gasPrice *big.Int,
	data []byte,
) *Transaction {
	val := new(big.Int)
	if value != nil {
		val.Set(value)
	}
	gp := new(big.Int)
	if gasPrice != nil {
		gp.Set(gasPrice)
	}
	return &Transaction{
		Nonce:       nonce,
		From:        from,
		To:          to,
		Value:       val,
		GasLimit:    gasLimit,
		GasPrice:    gp,
		Data:        append([]byte(nil), data...),
		StateReads:  make([]string, 0),
		StateWrites: make([]string, 0),
	}
}

// Hash returns the deterministic SHA-256 digest of the transaction fields.
func (tx *Transaction) Hash() Hash {
	tx.once.Do(func() {
		hasher := sha256.New()
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], tx.Nonce)
		hasher.Write(buf[:])
		hasher.Write(tx.From.Bytes())
		hasher.Write(tx.To.Bytes())
		if tx.Value != nil {
			hasher.Write(tx.Value.Bytes())
		}
		binary.BigEndian.PutUint64(buf[:], tx.GasLimit)
		hasher.Write(buf[:])
		if tx.GasPrice != nil {
			hasher.Write(tx.GasPrice.Bytes())
		}
		hasher.Write(tx.Data)
		copy(tx.hash[:], hasher.Sum(nil))
	})
	return tx.hash
}

// Bundle represents an ordered sequence of searcher transactions with atomicity constraints.
type Bundle struct {
	ID                string         `json:"id"`
	Searcher          Address        `json:"searcher"`
	Transactions      []*Transaction `json:"transactions"`
	TargetBlock       uint64         `json:"target_block"`
	MinTimestamp      uint64         `json:"min_timestamp"`
	MaxTimestamp      uint64         `json:"max_timestamp"`
	RevertingTxHashes []Hash         `json:"reverting_tx_hashes"`
	BidAmount         *big.Int       `json:"bid_amount"` // Direct payment to builder/proposer
}

// TotalGasLimit returns the sum of gas limits of all transactions in the bundle.
func (b *Bundle) TotalGasLimit() uint64 {
	var total uint64
	for _, tx := range b.Transactions {
		total += tx.GasLimit
	}
	return total
}

// Hash returns a commitment over the bundle identity and contents.
func (b *Bundle) Hash() Hash {
	hasher := sha256.New()
	hasher.Write([]byte(b.ID))
	hasher.Write(b.Searcher.Bytes())
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], b.TargetBlock)
	hasher.Write(buf[:])
	for _, tx := range b.Transactions {
		h := tx.Hash()
		hasher.Write(h[:])
	}
	if b.BidAmount != nil {
		hasher.Write(b.BidAmount.Bytes())
	}
	var out Hash
	copy(out[:], hasher.Sum(nil))
	return out
}

// ExecutionPayload contains the full unblinded block execution contents.
type ExecutionPayload struct {
	ParentHash    Hash           `json:"parent_hash"`
	FeeRecipient  Address        `json:"fee_recipient"`
	StateRoot     Hash           `json:"state_root"`
	ReceiptsRoot  Hash           `json:"receipts_root"`
	BlockNumber   uint64         `json:"block_number"`
	GasLimit      uint64         `json:"gas_limit"`
	GasUsed       uint64         `json:"gas_used"`
	Timestamp     uint64         `json:"timestamp"`
	ExtraData     []byte         `json:"extra_data"`
	BaseFeePerGas *big.Int       `json:"base_fee_per_gas"`
	BlockHash     Hash           `json:"block_hash"`
	Transactions  []*Transaction `json:"transactions"`
}

// ComputeTransactionsRoot computes the Merkle root of the included transactions.
func (p *ExecutionPayload) ComputeTransactionsRoot() Hash {
	if len(p.Transactions) == 0 {
		return Hash{}
	}
	hashes := make([]Hash, len(p.Transactions))
	for i, tx := range p.Transactions {
		hashes[i] = tx.Hash()
	}
	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}
		nextLevel := make([]Hash, len(hashes)/2)
		for i := 0; i < len(hashes); i += 2 {
			hasher := sha256.New()
			hasher.Write(hashes[i][:])
			hasher.Write(hashes[i+1][:])
			copy(nextLevel[i/2][:], hasher.Sum(nil))
		}
		hashes = nextLevel
	}
	return hashes[0]
}

// ComputeBlockHash computes the canonical block hash over the header fields.
func (p *ExecutionPayload) ComputeBlockHash() Hash {
	hasher := sha256.New()
	hasher.Write(p.ParentHash[:])
	hasher.Write(p.FeeRecipient[:])
	hasher.Write(p.StateRoot[:])
	hasher.Write(p.ReceiptsRoot[:])
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], p.BlockNumber)
	hasher.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], p.GasLimit)
	hasher.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], p.GasUsed)
	hasher.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], p.Timestamp)
	hasher.Write(buf[:])
	hasher.Write(p.ExtraData)
	if p.BaseFeePerGas != nil {
		hasher.Write(p.BaseFeePerGas.Bytes())
	}
	txRoot := p.ComputeTransactionsRoot()
	hasher.Write(txRoot[:])
	var out Hash
	copy(out[:], hasher.Sum(nil))
	p.BlockHash = out
	return out
}

// ExecutionPayloadHeader represents the blinded payload header served to validators.
type ExecutionPayloadHeader struct {
	ParentHash       Hash     `json:"parent_hash"`
	FeeRecipient     Address  `json:"fee_recipient"`
	StateRoot        Hash     `json:"state_root"`
	ReceiptsRoot     Hash     `json:"receipts_root"`
	BlockNumber      uint64   `json:"block_number"`
	GasLimit         uint64   `json:"gas_limit"`
	GasUsed          uint64   `json:"gas_used"`
	Timestamp        uint64   `json:"timestamp"`
	BaseFeePerGas    *big.Int `json:"base_fee_per_gas"`
	BlockHash        Hash     `json:"block_hash"`
	TransactionsRoot Hash     `json:"transactions_root"`
}

// ToHeader extracts the blinded header from an unblinded execution payload.
func (p *ExecutionPayload) ToHeader() *ExecutionPayloadHeader {
	bf := new(big.Int)
	if p.BaseFeePerGas != nil {
		bf.Set(p.BaseFeePerGas)
	}
	return &ExecutionPayloadHeader{
		ParentHash:       p.ParentHash,
		FeeRecipient:     p.FeeRecipient,
		StateRoot:        p.StateRoot,
		ReceiptsRoot:     p.ReceiptsRoot,
		BlockNumber:      p.BlockNumber,
		GasLimit:         p.GasLimit,
		GasUsed:          p.GasUsed,
		Timestamp:        p.Timestamp,
		BaseFeePerGas:    bf,
		BlockHash:        p.BlockHash,
		TransactionsRoot: p.ComputeTransactionsRoot(),
	}
}

// BuilderBid represents a builder's sealed bid for block space.
type BuilderBid struct {
	Header       *ExecutionPayloadHeader `json:"header"`
	Value        *big.Int                `json:"value"` // Total ETH payment to feeRecipient
	BuilderID    string                  `json:"builder_id"`
	Slot         uint64                  `json:"slot"`
	Timestamp    uint64                  `json:"timestamp"`
	Signature    []byte                  `json:"signature"`
}

// SignedBlindedBeaconBlock represents a proposer's commitment to the blinded header.
type SignedBlindedBeaconBlock struct {
	Header         *ExecutionPayloadHeader `json:"header"`
	Slot           uint64                  `json:"slot"`
	ProposerPubkey string                  `json:"proposer_pubkey"`
	Signature      []byte                  `json:"signature"`
}

// VerifyEquivocation checks if two signed blocks from the same proposer conflict on the same slot.
func VerifyEquivocation(b1, b2 *SignedBlindedBeaconBlock) bool {
	if b1.Slot != b2.Slot {
		return false
	}
	if b1.ProposerPubkey != b2.ProposerPubkey {
		return false
	}
	// Equivocation occurs when different headers are signed for the same slot
	return !bytes.Equal(b1.Header.BlockHash[:], b2.Header.BlockHash[:])
}
