// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title KRWTestStablecoin (tKRW)
/// @notice A demo KRW stablecoin. It models an issuance and distribution flow in which
///         the issuer mints and burns against off-chain reserves, and it implements
///         EIP-3009 transferWithAuthorization, the settlement path used by x402 payments.
///         This is a minimal contract for verifying the flow, not a production token.
contract KRWTestStablecoin {
    string public constant name = "KRW Test Stablecoin";
    string public constant symbol = "tKRW";
    uint8 public constant decimals = 0; // 1 tKRW = 1 KRW, simplified for the demo

    address public issuer;
    bool public paused;

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    // EIP-3009: tracks whether a random 32-byte nonce has been used (replay protection)
    mapping(address => mapping(bytes32 => bool)) public authorizationState;

    // EIP-712 domain
    bytes32 public immutable DOMAIN_SEPARATOR;
    bytes32 public constant TRANSFER_WITH_AUTHORIZATION_TYPEHASH =
        keccak256(
            "TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"
        );

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);
    event Mint(address indexed to, uint256 value);
    event Burn(address indexed from, uint256 value);
    event AuthorizationUsed(address indexed authorizer, bytes32 indexed nonce);
    event PauseSet(bool paused);
    event IssuerTransferStarted(address indexed current, address indexed pending);
    event IssuerTransferred(address indexed previous, address indexed current);

    address public pendingIssuer; // two-step issuer handover

    modifier onlyIssuer() {
        require(msg.sender == issuer, "tKRW: not issuer");
        _;
    }

    modifier whenNotPaused() {
        require(!paused, "tKRW: paused");
        _;
    }

    constructor() {
        issuer = msg.sender;
        DOMAIN_SEPARATOR = keccak256(
            abi.encode(
                keccak256(
                    "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
                ),
                keccak256(bytes(name)),
                keccak256(bytes("1")),
                block.chainid,
                address(this)
            )
        );
    }

    // ---- Issuance and distribution (issuer only) ----

    function mint(address to, uint256 value) external onlyIssuer whenNotPaused {
        require(to != address(0), "tKRW: mint to zero");
        totalSupply += value;
        balanceOf[to] += value;
        emit Mint(to, value);
        emit Transfer(address(0), to, value);
    }

    function burn(address from, uint256 value) external onlyIssuer whenNotPaused {
        require(balanceOf[from] >= value, "tKRW: burn exceeds balance");
        balanceOf[from] -= value;
        totalSupply -= value;
        emit Burn(from, value);
        emit Transfer(from, address(0), value);
    }

    function setPaused(bool p) external onlyIssuer {
        paused = p;
        emit PauseSet(p);
    }

    function transferIssuer(address newIssuer) external onlyIssuer {
        pendingIssuer = newIssuer;
        emit IssuerTransferStarted(issuer, newIssuer);
    }

    function acceptIssuer() external {
        require(msg.sender == pendingIssuer, "tKRW: not pending issuer");
        emit IssuerTransferred(issuer, pendingIssuer);
        issuer = pendingIssuer;
        pendingIssuer = address(0);
    }

    // ---- ERC-20 ----

    function transfer(address to, uint256 value) external whenNotPaused returns (bool) {
        _transfer(msg.sender, to, value);
        return true;
    }

    function approve(address spender, uint256 value) external whenNotPaused returns (bool) {
        allowance[msg.sender][spender] = value;
        emit Approval(msg.sender, spender, value);
        return true;
    }

    function transferFrom(address from, address to, uint256 value)
        external
        whenNotPaused
        returns (bool)
    {
        uint256 allowed = allowance[from][msg.sender];
        require(allowed >= value, "tKRW: insufficient allowance");
        if (allowed != type(uint256).max) {
            allowance[from][msg.sender] = allowed - value;
        }
        _transfer(from, to, value);
        return true;
    }

    function _transfer(address from, address to, uint256 value) internal {
        require(to != address(0), "tKRW: transfer to zero");
        require(balanceOf[from] >= value, "tKRW: insufficient balance");
        balanceOf[from] -= value;
        balanceOf[to] += value;
        emit Transfer(from, to, value);
    }

    // ---- EIP-3009: settlement path for x402 ----
    // The payer (an agent) only produces a signature; the gateway acting as
    // facilitator submits the transaction and pays for gas.

    function transferWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external whenNotPaused {
        require(block.timestamp > validAfter, "tKRW: authorization not yet valid");
        require(block.timestamp < validBefore, "tKRW: authorization expired");
        require(!authorizationState[from][nonce], "tKRW: authorization used");

        bytes32 structHash = keccak256(
            abi.encode(
                TRANSFER_WITH_AUTHORIZATION_TYPEHASH,
                from,
                to,
                value,
                validAfter,
                validBefore,
                nonce
            )
        );
        bytes32 digest = keccak256(
            abi.encodePacked("\x19\x01", DOMAIN_SEPARATOR, structHash)
        );
        address recovered = ecrecover(digest, v, r, s);
        require(recovered != address(0) && recovered == from, "tKRW: invalid signature");

        authorizationState[from][nonce] = true;
        emit AuthorizationUsed(from, nonce);
        _transfer(from, to, value);
    }
}
