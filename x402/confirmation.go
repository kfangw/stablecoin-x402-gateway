package x402

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// A confirmation is a delegator's signed approval of one specific payment that
// exceeded a mandate's limits. It shares the mandate EIP-712 domain (so it is
// chain-bound) and names the payment's authorization nonce, so it authorizes
// exactly that payment and cannot be reused for another.
var confirmationTypeHash = crypto.Keccak256Hash(
	[]byte("Confirmation(bytes32 mandateId,bytes32 authorizationNonce,uint256 amount,string resource,uint256 validBefore)"),
)

// Confirmation is a delegator's approval of a single over-limit payment.
type Confirmation struct {
	MandateID          [32]byte
	AuthorizationNonce [32]byte
	Amount             *big.Int
	Resource           string
	ValidBefore        *big.Int
}

// ConfirmationJSON is the wire form carried in the payment payload.
type ConfirmationJSON struct {
	MandateID          string `json:"mandateId"`
	AuthorizationNonce string `json:"authorizationNonce"`
	Amount             string `json:"amount"`
	Resource           string `json:"resource"`
	ValidBefore        string `json:"validBefore"`
	Signature          string `json:"signature"`
}

func (c Confirmation) structHash() [32]byte {
	buf := make([]byte, 0, 6*32)
	buf = append(buf, confirmationTypeHash.Bytes()...)
	buf = append(buf, c.MandateID[:]...)
	buf = append(buf, c.AuthorizationNonce[:]...)
	buf = append(buf, u256(c.Amount)...)
	buf = append(buf, crypto.Keccak256([]byte(c.Resource))...)
	buf = append(buf, u256(c.ValidBefore)...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// SignConfirmation produces the delegator's signature over the confirmation.
func SignConfirmation(key *ecdsa.PrivateKey, c Confirmation, chainID *big.Int) ([]byte, error) {
	digest := wallet.Digest(mandateDomainSeparator(chainID), c.structHash())
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return nil, fmt.Errorf("x402: sign confirmation: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

// VerifyConfirmation recovers and returns the address that signed a
// confirmation. The caller checks it against the mandate's delegator.
func VerifyConfirmation(c Confirmation, sig []byte, chainID *big.Int) (common.Address, error) {
	signer, err := recover712(mandateDomainSeparator(chainID), c.structHash(), sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("x402: confirmation: %w", err)
	}
	return signer, nil
}

// ToJSON converts a confirmation to its wire form (without a signature).
func (c Confirmation) ToJSON() ConfirmationJSON {
	return ConfirmationJSON{
		MandateID:          "0x" + common.Bytes2Hex(c.MandateID[:]),
		AuthorizationNonce: "0x" + common.Bytes2Hex(c.AuthorizationNonce[:]),
		Amount:             bigString(c.Amount),
		Resource:           c.Resource,
		ValidBefore:        bigString(c.ValidBefore),
	}
}

// ToConfirmation parses the confirmation fields from their wire form. The
// signature is handled separately by the caller.
func (j ConfirmationJSON) ToConfirmation() (Confirmation, error) {
	id, err := parseBytes32(j.MandateID)
	if err != nil {
		return Confirmation{}, fmt.Errorf("x402: confirmation mandate id: %w", err)
	}
	nonce, err := parseBytes32(j.AuthorizationNonce)
	if err != nil {
		return Confirmation{}, fmt.Errorf("x402: confirmation nonce: %w", err)
	}
	amount, ok := new(big.Int).SetString(j.Amount, 10)
	if !ok {
		return Confirmation{}, fmt.Errorf("x402: confirmation amount %q is not an integer", j.Amount)
	}
	validBefore, ok := new(big.Int).SetString(j.ValidBefore, 10)
	if !ok {
		return Confirmation{}, fmt.Errorf("x402: confirmation validBefore %q is not an integer", j.ValidBefore)
	}
	return Confirmation{
		MandateID:          id,
		AuthorizationNonce: nonce,
		Amount:             amount,
		Resource:           j.Resource,
		ValidBefore:        validBefore,
	}, nil
}
