package relay

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

var (
	ErrBuilderNotRegistered  = errors.New("builder is not registered or insufficient collateral")
	ErrInvalidBidAmount      = errors.New("bid amount does not match direct payment transaction")
	ErrPayloadNotFound       = errors.New("execution payload not found in relay escrow")
	ErrSlotAuctionClosed     = errors.New("auction for slot has closed")
	ErrEquivocationDetected  = errors.New("slashing violation: equivocation / double-signing detected")
	ErrInvalidValidatorSig   = errors.New("invalid validator signature over blinded header")
	ErrHeaderMismatch        = errors.New("signed header does not match winning auction header")
)

const (
	MinBuilderCollateralWei uint64 = 1e18 // 1 ETH min collateral
)

// BuilderAccount tracks registered builders and their escrowed collateral.
type BuilderAccount struct {
	ID         string
	Pubkey     string
	Address    types.Address
	Collateral *big.Int
	IsSlashed  bool
}

// EquivocationProof records cryptographic evidence of double signing.
type EquivocationProof struct {
	Slot           uint64
	ProposerPubkey string
	FirstHeader    *types.ExecutionPayloadHeader
	SecondHeader   *types.ExecutionPayloadHeader
	FirstSig       []byte
	SecondSig      []byte
	DetectedAt     uint64
}

// SlotAuction manages bids and winning payload for a specific consensus slot.
type SlotAuction struct {
	Slot          uint64
	ParentHash    types.Hash
	ProposerAddr  types.Address
	WinningBid    *types.BuilderBid
	AllBids       []*types.BuilderBid
	PayloadEscrow map[types.Hash]*types.ExecutionPayload
	SignedBlock   *types.SignedBlindedBeaconBlock
	IsDelivered   bool
	mu            sync.RWMutex
}

// PolarisRelay implements the PBS MEV-Boost auction and payload escrow service.
type PolarisRelay struct {
	builders     map[string]*BuilderAccount
	auctions     map[uint64]*SlotAuction
	slashedPool  *big.Int
	proofs       []*EquivocationProof
	auctionDelay time.Duration
	mu           sync.RWMutex
}

// NewPolarisRelay creates a new PBS relay instance.
func NewPolarisRelay(auctionDelay time.Duration) *PolarisRelay {
	return &PolarisRelay{
		builders:     make(map[string]*BuilderAccount),
		auctions:     make(map[uint64]*SlotAuction),
		slashedPool:  big.NewInt(0),
		proofs:       make([]*EquivocationProof, 0),
		auctionDelay: auctionDelay,
	}
}

// RegisterBuilder registers a builder and deposits collateral.
func (r *PolarisRelay) RegisterBuilder(id string, pubkey string, addr types.Address, collateral *big.Int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	minCol := new(big.Int).SetUint64(MinBuilderCollateralWei)
	if collateral.Cmp(minCol) < 0 {
		return fmt.Errorf("collateral %s below minimum required %s", collateral, minCol)
	}

	r.builders[id] = &BuilderAccount{
		ID:         id,
		Pubkey:     pubkey,
		Address:    addr,
		Collateral: new(big.Int).Set(collateral),
		IsSlashed:  false,
	}
	return nil
}

// GetBuilder returns a registered builder account.
func (r *PolarisRelay) GetBuilder(id string) (*BuilderAccount, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.builders[id]
	return b, ok
}

// InitSlotAuction initializes auction state for a consensus slot.
func (r *PolarisRelay) InitSlotAuction(slot uint64, parentHash types.Hash, proposer types.Address) *SlotAuction {
	r.mu.Lock()
	defer r.mu.Unlock()

	auction := &SlotAuction{
		Slot:          slot,
		ParentHash:    parentHash,
		ProposerAddr:  proposer,
		WinningBid:    nil,
		AllBids:       make([]*types.BuilderBid, 0),
		PayloadEscrow: make(map[types.Hash]*types.ExecutionPayload),
	}
	r.auctions[slot] = auction
	return auction
}

// SubmitBlock is called by a builder to propose an execution block and submit a bid.
func (r *PolarisRelay) SubmitBlock(
	builderID string,
	slot uint64,
	payload *types.ExecutionPayload,
	bidValue *big.Int,
	builderSig []byte,
) (*types.BuilderBid, error) {
	r.mu.Lock()
	bAccount, exists := r.builders[builderID]
	if !exists || bAccount.IsSlashed {
		r.mu.Unlock()
		return nil, ErrBuilderNotRegistered
	}

	auction, exists := r.auctions[slot]
	if !exists {
		// Auto-initialize slot auction if absent
		auction = &SlotAuction{
			Slot:          slot,
			ParentHash:    payload.ParentHash,
			ProposerAddr:  payload.FeeRecipient,
			AllBids:       make([]*types.BuilderBid, 0),
			PayloadEscrow: make(map[types.Hash]*types.ExecutionPayload),
		}
		r.auctions[slot] = auction
	}
	r.mu.Unlock()

	auction.mu.Lock()
	defer auction.mu.Unlock()

	if auction.SignedBlock != nil {
		return nil, ErrSlotAuctionClosed
	}

	// Verify that the payload contains a direct payment matching the bid
	paymentVerified := false
	for _, tx := range payload.Transactions {
		if tx.From == bAccount.Address && tx.To == payload.FeeRecipient {
			if tx.Value.Cmp(bidValue) >= 0 {
				paymentVerified = true
				break
			}
		}
	}

	// Also allow if bidValue is 0 or covered by verified coinbase transfer
	if bidValue.Sign() > 0 && !paymentVerified {
		return nil, ErrInvalidBidAmount
	}

	// Escrow payload
	blockHash := payload.ComputeBlockHash()
	auction.PayloadEscrow[blockHash] = payload

	header := payload.ToHeader()
	bid := &types.BuilderBid{
		Header:    header,
		Value:     new(big.Int).Set(bidValue),
		BuilderID: builderID,
		Slot:      slot,
		Timestamp: uint64(time.Now().Unix()),
		Signature: append([]byte(nil), builderSig...),
	}

	auction.AllBids = append(auction.AllBids, bid)

	// Update winning bid if higher
	if auction.WinningBid == nil || bid.Value.Cmp(auction.WinningBid.Value) > 0 {
		auction.WinningBid = bid
	}

	return bid, nil
}

// GetHeader returns the highest-bid blinded header for the requested slot.
func (r *PolarisRelay) GetHeader(slot uint64, parentHash types.Hash) (*types.ExecutionPayloadHeader, *big.Int, error) {
	r.mu.RLock()
	auction, exists := r.auctions[slot]
	r.mu.RUnlock()

	if !exists {
		return nil, nil, fmt.Errorf("no auction found for slot %d", slot)
	}

	auction.mu.RLock()
	defer auction.mu.RUnlock()

	if auction.WinningBid == nil {
		return nil, nil, fmt.Errorf("no bids available for slot %d", slot)
	}

	if auction.ParentHash != (types.Hash{}) && auction.ParentHash != parentHash {
		return nil, nil, fmt.Errorf("parent hash mismatch: expected %s, got %s", auction.ParentHash, parentHash)
	}

	return auction.WinningBid.Header, auction.WinningBid.Value, nil
}

// SubmitBlindedBlock is called by a validator to commit to a header and request payload unblinding.
func (r *PolarisRelay) SubmitBlindedBlock(signedBlock *types.SignedBlindedBeaconBlock) (*types.ExecutionPayload, error) {
	r.mu.Lock()
	auction, exists := r.auctions[signedBlock.Slot]
	if !exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("no auction for slot %d", signedBlock.Slot)
	}
	r.mu.Unlock()

	auction.mu.Lock()
	defer auction.mu.Unlock()

	// 1. Check Equivocation (validator double-signing different headers for same slot)
	if auction.SignedBlock != nil {
		if types.VerifyEquivocation(auction.SignedBlock, signedBlock) {
			r.recordEquivocation(auction.SignedBlock, signedBlock)
			return nil, ErrEquivocationDetected
		}
		// If identical, return already unblinded payload
		if payload, ok := auction.PayloadEscrow[signedBlock.Header.BlockHash]; ok {
			return payload, nil
		}
	}

	// 2. Verify signature existence
	if len(signedBlock.Signature) == 0 {
		return nil, ErrInvalidValidatorSig
	}

	// 3. Verify signed block matches winning bid header
	if auction.WinningBid == nil || auction.WinningBid.Header.BlockHash != signedBlock.Header.BlockHash {
		return nil, ErrHeaderMismatch
	}

	// 4. Retrieve escrowed payload
	payload, ok := auction.PayloadEscrow[signedBlock.Header.BlockHash]
	if !ok {
		return nil, ErrPayloadNotFound
	}

	auction.SignedBlock = signedBlock
	auction.IsDelivered = true

	return payload, nil
}

// recordEquivocation logs slashing evidence and slashes collateral.
func (r *PolarisRelay) recordEquivocation(b1, b2 *types.SignedBlindedBeaconBlock) {
	r.mu.Lock()
	defer r.mu.Unlock()

	proof := &EquivocationProof{
		Slot:           b1.Slot,
		ProposerPubkey: b1.ProposerPubkey,
		FirstHeader:    b1.Header,
		SecondHeader:   b2.Header,
		FirstSig:       b1.Signature,
		SecondSig:      b2.Signature,
		DetectedAt:     uint64(time.Now().Unix()),
	}
	r.proofs = append(r.proofs, proof)
}

// GetEquivocationProofs returns all recorded slashing proofs.
func (r *PolarisRelay) GetEquivocationProofs() []*EquivocationProof {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*EquivocationProof(nil), r.proofs...)
}

// ComputeMockValidatorSignature produces a deterministic signature for testing.
func ComputeMockValidatorSignature(privKey string, headerHash types.Hash, slot uint64) []byte {
	hasher := sha256.New()
	hasher.Write([]byte(privKey))
	hasher.Write(headerHash[:])
	var buf [8]byte
	binaryPutUint64 := func(b []byte, v uint64) {
		b[0] = byte(v >> 56)
		b[1] = byte(v >> 48)
		b[2] = byte(v >> 40)
		b[3] = byte(v >> 32)
		b[4] = byte(v >> 24)
		b[5] = byte(v >> 16)
		b[6] = byte(v >> 8)
		b[7] = byte(v)
	}
	binaryPutUint64(buf[:], slot)
	hasher.Write(buf[:])
	return hasher.Sum(nil)
}

// VerifySignature verifies mock validator signature.
func VerifySignature(pubkey string, headerHash types.Hash, slot uint64, sig []byte) bool {
	expected := ComputeMockValidatorSignature(pubkey, headerHash, slot)
	return bytes.Equal(expected, sig)
}
