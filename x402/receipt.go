package x402

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

const (
	receiptDomainName    = "stablecoin-x402-gateway receipt"
	receiptDomainVersion = "1"
)

var (
	receiptDomainTypeHash = crypto.Keccak256Hash(
		[]byte("EIP712Domain(string name,string version,uint256 chainId)"),
	)
	receiptTypeHash = crypto.Keccak256Hash(
		[]byte("Receipt(bytes32 receiptId,string network,string resource,address payer,address payTo,uint256 amount,bytes32 settlementTx,bytes32 deliveryTx,bytes32 mandateId,address delegator,uint256 issuedAt)"),
	)
)

// Receipt links a settlement to the delegation it fulfilled: the mandate it was
// authorized under, the settlement transaction, and any asset delivery. The
// gateway signs it with a receipt-only key (never a chain key), so a keyless
// gateway can still issue receipts and an auditor verifies the whole chain
// offline from the gateway's public address alone.
type Receipt struct {
	ReceiptID    [32]byte
	Network      string
	Resource     string
	Payer        common.Address
	PayTo        common.Address
	Amount       *big.Int
	SettlementTx common.Hash
	DeliveryTx   common.Hash    // zero on the DvP and direct-settlement paths
	MandateID    [32]byte       // zero when the payment carried no mandate
	Delegator    common.Address // zero when the payment carried no mandate
	IssuedAt     int64
}

// ReceiptJSON is the wire form of a receipt.
type ReceiptJSON struct {
	ReceiptID    string `json:"receiptId"`
	Network      string `json:"network"`
	Resource     string `json:"resource"`
	Payer        string `json:"payer"`
	PayTo        string `json:"payTo"`
	Amount       string `json:"amount"`
	SettlementTx string `json:"settlementTx"`
	DeliveryTx   string `json:"deliveryTx,omitempty"`
	MandateID    string `json:"mandateId,omitempty"`
	Delegator    string `json:"delegator,omitempty"`
	IssuedAt     int64  `json:"issuedAt"`
}

// SignedReceiptJSON is a receipt plus the gateway's signature over it.
type SignedReceiptJSON struct {
	Receipt   ReceiptJSON `json:"receipt"`
	Signature string      `json:"signature"`
}

func receiptDomainSeparator(chainID *big.Int) [32]byte {
	buf := make([]byte, 0, 4*32)
	buf = append(buf, receiptDomainTypeHash.Bytes()...)
	buf = append(buf, crypto.Keccak256([]byte(receiptDomainName))...)
	buf = append(buf, crypto.Keccak256([]byte(receiptDomainVersion))...)
	buf = append(buf, u256(chainID)...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

func (r Receipt) structHash() [32]byte {
	buf := make([]byte, 0, 12*32)
	buf = append(buf, receiptTypeHash.Bytes()...)
	buf = append(buf, r.ReceiptID[:]...)
	buf = append(buf, crypto.Keccak256([]byte(r.Network))...)
	buf = append(buf, crypto.Keccak256([]byte(r.Resource))...)
	buf = append(buf, common.LeftPadBytes(r.Payer.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(r.PayTo.Bytes(), 32)...)
	buf = append(buf, u256(r.Amount)...)
	buf = append(buf, r.SettlementTx.Bytes()...)
	buf = append(buf, r.DeliveryTx.Bytes()...)
	buf = append(buf, r.MandateID[:]...)
	buf = append(buf, common.LeftPadBytes(r.Delegator.Bytes(), 32)...)
	buf = append(buf, u256(big.NewInt(r.IssuedAt))...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// SignReceipt signs a receipt with the gateway's receipt key. The chain id is
// read from the receipt's network, so the signature is self-contained.
func SignReceipt(key *ecdsa.PrivateKey, r Receipt) ([]byte, error) {
	chainID, err := chainIDFromNetwork(r.Network)
	if err != nil {
		return nil, err
	}
	digest := wallet.Digest(receiptDomainSeparator(chainID), r.structHash())
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return nil, fmt.Errorf("x402: sign receipt: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

// VerifyReceipt recovers the address that signed a receipt.
func VerifyReceipt(r Receipt, sig []byte) (common.Address, error) {
	chainID, err := chainIDFromNetwork(r.Network)
	if err != nil {
		return common.Address{}, err
	}
	return recover712(receiptDomainSeparator(chainID), r.structHash(), sig)
}

// ToJSON projects a receipt into its wire form.
func (r Receipt) ToJSON() ReceiptJSON {
	j := ReceiptJSON{
		ReceiptID:    "0x" + hex.EncodeToString(r.ReceiptID[:]),
		Network:      r.Network,
		Resource:     r.Resource,
		Payer:        r.Payer.Hex(),
		PayTo:        r.PayTo.Hex(),
		Amount:       bigString(r.Amount),
		SettlementTx: r.SettlementTx.Hex(),
		IssuedAt:     r.IssuedAt,
	}
	if r.DeliveryTx != (common.Hash{}) {
		j.DeliveryTx = r.DeliveryTx.Hex()
	}
	if r.MandateID != [32]byte{} {
		j.MandateID = "0x" + hex.EncodeToString(r.MandateID[:])
	}
	if r.Delegator != (common.Address{}) {
		j.Delegator = r.Delegator.Hex()
	}
	return j
}

// ToReceipt parses a receipt from its wire form.
func (j ReceiptJSON) ToReceipt() (Receipt, error) {
	amount, ok := new(big.Int).SetString(j.Amount, 10)
	if !ok {
		return Receipt{}, fmt.Errorf("x402: receipt amount %q is not an integer", j.Amount)
	}
	r := Receipt{
		Network:      j.Network,
		Resource:     j.Resource,
		Payer:        common.HexToAddress(j.Payer),
		PayTo:        common.HexToAddress(j.PayTo),
		Amount:       amount,
		SettlementTx: common.HexToHash(j.SettlementTx),
		IssuedAt:     j.IssuedAt,
	}
	id, err := hexTo32(j.ReceiptID)
	if err != nil {
		return Receipt{}, fmt.Errorf("x402: receipt id: %w", err)
	}
	r.ReceiptID = id
	if j.DeliveryTx != "" {
		r.DeliveryTx = common.HexToHash(j.DeliveryTx)
	}
	if j.MandateID != "" {
		if r.MandateID, err = hexTo32(j.MandateID); err != nil {
			return Receipt{}, fmt.Errorf("x402: receipt mandate id: %w", err)
		}
	}
	if j.Delegator != "" {
		r.Delegator = common.HexToAddress(j.Delegator)
	}
	return r, nil
}

// newReceiptID returns a random receipt id.
func newReceiptID() ([32]byte, error) {
	var id [32]byte
	_, err := rand.Read(id[:])
	return id, err
}

func hexTo32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("expected 32-byte hex, got %q", s)
	}
	copy(out[:], b)
	return out, nil
}

// chainIDFromNetwork parses the numeric chain id from an eip155:<id> network.
func chainIDFromNetwork(network string) (*big.Int, error) {
	_, id, ok := strings.Cut(network, ":")
	if !ok {
		return nil, fmt.Errorf("x402: cannot read chain id from network %q", network)
	}
	chainID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return nil, fmt.Errorf("x402: cannot read chain id from network %q", network)
	}
	return chainID, nil
}
