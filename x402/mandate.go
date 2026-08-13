package x402

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// A mandate is a delegator-signed, EIP-712 typed authorization that bounds what
// an agent may spend on the delegator's behalf. It travels with the payment, so
// the counterparty can check the agent's spending authority. The scheme reuses
// the repository's EIP-712 signing; the domain carries a chain ID so a mandate
// cannot be replayed on another chain.
const (
	mandateDomainName    = "stablecoin-x402-gateway mandate"
	mandateDomainVersion = "1"
)

var (
	mandateDomainTypeHash = crypto.Keccak256Hash(
		[]byte("EIP712Domain(string name,string version,uint256 chainId)"),
	)
	mandateTypeHash = crypto.Keccak256Hash(
		[]byte("Mandate(address delegator,address agent,uint256 maxAmountPerPayment,address[] allowedPayees,string[] allowedResources,uint256 validAfter,uint256 validBefore,uint256 budgetAmount,uint256 budgetWindowSeconds,uint256 maxPaymentsPerWindow,uint256 rateWindowSeconds,bytes32 mandateId)"),
	)
	revocationTypeHash = crypto.Keccak256Hash(
		[]byte("Revocation(bytes32 mandateId)"),
	)
)

// Mandate is a delegator's signed grant of spending authority to an agent.
type Mandate struct {
	Delegator           common.Address
	Agent               common.Address
	MaxAmountPerPayment *big.Int
	AllowedPayees       []common.Address
	AllowedResources    []string // prefix-matched against the request resource
	ValidAfter          *big.Int
	ValidBefore         *big.Int
	// Cumulative spending cap: at most BudgetAmount across each rolling window
	// of BudgetWindowSeconds. Zero BudgetAmount means no cumulative cap.
	BudgetAmount        *big.Int
	BudgetWindowSeconds *big.Int
	// Frequency cap: at most MaxPaymentsPerWindow settled payments across each
	// rolling window of RateWindowSeconds. Zero means no frequency cap.
	MaxPaymentsPerWindow *big.Int
	RateWindowSeconds    *big.Int
	MandateID            [32]byte
}

// MandateJSON is the wire form: decimal strings for integers, hex for addresses
// and the mandate id.
type MandateJSON struct {
	Delegator            string   `json:"delegator"`
	Agent                string   `json:"agent"`
	MaxAmountPerPayment  string   `json:"maxAmountPerPayment"`
	AllowedPayees        []string `json:"allowedPayees"`
	AllowedResources     []string `json:"allowedResources"`
	ValidAfter           string   `json:"validAfter"`
	ValidBefore          string   `json:"validBefore"`
	BudgetAmount         string   `json:"budgetAmount"`
	BudgetWindowSeconds  string   `json:"budgetWindowSeconds"`
	MaxPaymentsPerWindow string   `json:"maxPaymentsPerWindow"`
	RateWindowSeconds    string   `json:"rateWindowSeconds"`
	MandateID            string   `json:"mandateId"` // 0x + 32-byte hex
}

// SignedMandateJSON is a mandate plus the delegator's signature, as carried in
// the payment payload.
type SignedMandateJSON struct {
	Mandate   MandateJSON `json:"mandate"`
	Signature string      `json:"signature"` // 0x + 65-byte hex
}

// RevocationJSON is the body of a revocation request: the mandate id and the
// delegator's signature over it.
type RevocationJSON struct {
	MandateID string `json:"mandateId"`
	Signature string `json:"signature"`
}

func mandateDomainSeparator(chainID *big.Int) [32]byte {
	buf := make([]byte, 0, 4*32)
	buf = append(buf, mandateDomainTypeHash.Bytes()...)
	buf = append(buf, crypto.Keccak256([]byte(mandateDomainName))...)
	buf = append(buf, crypto.Keccak256([]byte(mandateDomainVersion))...)
	buf = append(buf, u256(chainID)...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// structHash is the EIP-712 struct hash. Dynamic fields (the arrays) are
// replaced by their own keccak256 as EIP-712 requires.
func (m Mandate) structHash() [32]byte {
	payees := make([]byte, 0, 32*len(m.AllowedPayees))
	for _, p := range m.AllowedPayees {
		payees = append(payees, common.LeftPadBytes(p.Bytes(), 32)...)
	}
	resources := make([]byte, 0, 32*len(m.AllowedResources))
	for _, r := range m.AllowedResources {
		resources = append(resources, crypto.Keccak256([]byte(r))...)
	}
	buf := make([]byte, 0, 13*32)
	buf = append(buf, mandateTypeHash.Bytes()...)
	buf = append(buf, common.LeftPadBytes(m.Delegator.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(m.Agent.Bytes(), 32)...)
	buf = append(buf, u256(m.MaxAmountPerPayment)...)
	buf = append(buf, crypto.Keccak256(payees)...)
	buf = append(buf, crypto.Keccak256(resources)...)
	buf = append(buf, u256(m.ValidAfter)...)
	buf = append(buf, u256(m.ValidBefore)...)
	buf = append(buf, u256(m.BudgetAmount)...)
	buf = append(buf, u256(m.BudgetWindowSeconds)...)
	buf = append(buf, u256(m.MaxPaymentsPerWindow)...)
	buf = append(buf, u256(m.RateWindowSeconds)...)
	buf = append(buf, m.MandateID[:]...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// SignMandate produces the delegator's 65-byte signature over the mandate.
func SignMandate(key *ecdsa.PrivateKey, m Mandate, chainID *big.Int) ([]byte, error) {
	digest := wallet.Digest(mandateDomainSeparator(chainID), m.structHash())
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return nil, fmt.Errorf("x402: sign mandate: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

// VerifyMandate recovers the signer and confirms it is the mandate's declared
// delegator, returning that address. A mismatch or a malformed signature is an
// error, so a mandate cannot claim a delegator it was not signed by.
func VerifyMandate(m Mandate, sig []byte, chainID *big.Int) (common.Address, error) {
	signer, err := recover712(mandateDomainSeparator(chainID), m.structHash(), sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("x402: mandate: %w", err)
	}
	if signer != m.Delegator {
		return common.Address{}, fmt.Errorf("x402: mandate signed by %s, not delegator %s", signer, m.Delegator)
	}
	return signer, nil
}

func revocationStructHash(mandateID [32]byte) [32]byte {
	buf := make([]byte, 0, 2*32)
	buf = append(buf, revocationTypeHash.Bytes()...)
	buf = append(buf, mandateID[:]...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// SignRevocation produces the delegator's signature over a mandate id.
func SignRevocation(key *ecdsa.PrivateKey, mandateID [32]byte, chainID *big.Int) ([]byte, error) {
	digest := wallet.Digest(mandateDomainSeparator(chainID), revocationStructHash(mandateID))
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return nil, fmt.Errorf("x402: sign revocation: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

// VerifyRevocation recovers and returns the address that signed a revocation of
// mandateID. The caller keys the revocation by (signer, mandateID), so only the
// mandate's own delegator can revoke it.
func VerifyRevocation(mandateID [32]byte, sig []byte, chainID *big.Int) (common.Address, error) {
	signer, err := recover712(mandateDomainSeparator(chainID), revocationStructHash(mandateID), sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("x402: revocation: %w", err)
	}
	return signer, nil
}

// recover712 recovers the signer of an EIP-712 digest from a 65-byte signature.
func recover712(domainSeparator, structHash [32]byte, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}
	digest := wallet.Digest(domainSeparator, structHash)
	cp := make([]byte, 65)
	copy(cp, sig)
	if cp[64] >= 27 {
		cp[64] -= 27
	}
	pub, err := crypto.SigToPub(digest[:], cp)
	if err != nil {
		return common.Address{}, fmt.Errorf("recover signer: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// u256 left-pads a non-negative integer to 32 bytes, treating nil as zero.
func u256(x *big.Int) []byte {
	if x == nil {
		return make([]byte, 32)
	}
	return common.LeftPadBytes(x.Bytes(), 32)
}

// ToJSON converts a mandate to its wire form.
func (m Mandate) ToJSON() MandateJSON {
	payees := make([]string, len(m.AllowedPayees))
	for i, p := range m.AllowedPayees {
		payees[i] = p.Hex()
	}
	resources := append([]string(nil), m.AllowedResources...)
	return MandateJSON{
		Delegator:            m.Delegator.Hex(),
		Agent:                m.Agent.Hex(),
		MaxAmountPerPayment:  bigString(m.MaxAmountPerPayment),
		AllowedPayees:        payees,
		AllowedResources:     resources,
		ValidAfter:           bigString(m.ValidAfter),
		ValidBefore:          bigString(m.ValidBefore),
		BudgetAmount:         bigString(m.BudgetAmount),
		BudgetWindowSeconds:  bigString(m.BudgetWindowSeconds),
		MaxPaymentsPerWindow: bigString(m.MaxPaymentsPerWindow),
		RateWindowSeconds:    bigString(m.RateWindowSeconds),
		MandateID:            "0x" + common.Bytes2Hex(m.MandateID[:]),
	}
}

// ToMandate parses a wire mandate into its native form.
func (j MandateJSON) ToMandate() (Mandate, error) {
	if !common.IsHexAddress(j.Delegator) {
		return Mandate{}, fmt.Errorf("x402: mandate delegator %q is not an address", j.Delegator)
	}
	if !common.IsHexAddress(j.Agent) {
		return Mandate{}, fmt.Errorf("x402: mandate agent %q is not an address", j.Agent)
	}
	payees := make([]common.Address, len(j.AllowedPayees))
	for i, p := range j.AllowedPayees {
		if !common.IsHexAddress(p) {
			return Mandate{}, fmt.Errorf("x402: mandate payee %q is not an address", p)
		}
		payees[i] = common.HexToAddress(p)
	}
	ints := map[string]*big.Int{}
	for name, s := range map[string]string{
		"maxAmountPerPayment":  j.MaxAmountPerPayment,
		"validAfter":           j.ValidAfter,
		"validBefore":          j.ValidBefore,
		"budgetAmount":         j.BudgetAmount,
		"budgetWindowSeconds":  j.BudgetWindowSeconds,
		"maxPaymentsPerWindow": j.MaxPaymentsPerWindow,
		"rateWindowSeconds":    j.RateWindowSeconds,
	} {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return Mandate{}, fmt.Errorf("x402: mandate %s %q is not an integer", name, s)
		}
		ints[name] = v
	}
	id, err := parseBytes32(j.MandateID)
	if err != nil {
		return Mandate{}, fmt.Errorf("x402: mandate id: %w", err)
	}
	return Mandate{
		Delegator:            common.HexToAddress(j.Delegator),
		Agent:                common.HexToAddress(j.Agent),
		MaxAmountPerPayment:  ints["maxAmountPerPayment"],
		AllowedPayees:        payees,
		AllowedResources:     append([]string(nil), j.AllowedResources...),
		ValidAfter:           ints["validAfter"],
		ValidBefore:          ints["validBefore"],
		BudgetAmount:         ints["budgetAmount"],
		BudgetWindowSeconds:  ints["budgetWindowSeconds"],
		MaxPaymentsPerWindow: ints["maxPaymentsPerWindow"],
		RateWindowSeconds:    ints["rateWindowSeconds"],
		MandateID:            id,
	}, nil
}

func bigString(x *big.Int) string {
	if x == nil {
		return "0"
	}
	return x.String()
}

// parseBytes32 parses a 0x-prefixed 32-byte hex string.
// WithinScope reports whether a payment to payTo for a resource at the given
// amount falls inside this mandate's entitlements: the payee allowlist, the
// resource prefixes, and the per-payment cap. It reuses the same checks the
// gateway's mandate policy applies, so an offline auditor reaches the same
// verdict. It does not check the cumulative or rate windows, which are stateful.
func (m Mandate) WithinScope(payTo common.Address, resource string, amount *big.Int) error {
	if len(m.AllowedPayees) > 0 && !containsAddress(m.AllowedPayees, payTo) {
		return fmt.Errorf("payee %s is not allowed by the mandate", payTo.Hex())
	}
	if len(m.AllowedResources) > 0 && !prefixAllowed(m.AllowedResources, resource) {
		return fmt.Errorf("resource %q is not allowed by the mandate", resource)
	}
	if m.MaxAmountPerPayment != nil && m.MaxAmountPerPayment.Sign() > 0 && amount.Cmp(m.MaxAmountPerPayment) > 0 {
		return fmt.Errorf("amount %s exceeds the per-payment limit %s", amount, m.MaxAmountPerPayment)
	}
	return nil
}

func parseBytes32(s string) ([32]byte, error) {
	var out [32]byte
	b := common.FromHex(s)
	if len(b) != 32 {
		return out, fmt.Errorf("want 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
