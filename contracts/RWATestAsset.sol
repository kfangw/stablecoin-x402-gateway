// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IEligibilityRegistry {
    function isEligible(address account) external view returns (bool);
}

/// @title RWATestAsset (tRWA)
/// @notice A demo real-world-asset token delivered to a payer after settlement.
///         It reuses the minimal tKRW skeleton: an ERC-20 with issuer-only mint
///         and burn and the standard Transfer event, so the same ledger tooling
///         reconciles it. It carries an eligibility registry address so transfers
///         can be gated on recipient eligibility, in the spirit of ERC-3643; the
///         transfer-time check itself is added in a later change. A zero registry
///         means transfers are unrestricted, which keeps tests simple.
///         This is a minimal contract for verifying the flow, not production code.
contract RWATestAsset {
    string public constant name = "RWA Test Asset";
    string public constant symbol = "tRWA";
    uint8 public constant decimals = 0;

    address public issuer;
    address public immutable registry;

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);
    event Mint(address indexed to, uint256 value);
    event Burn(address indexed from, uint256 value);

    modifier onlyIssuer() {
        require(msg.sender == issuer, "tRWA: not issuer");
        _;
    }

    constructor(address eligibilityRegistry) {
        issuer = msg.sender;
        registry = eligibilityRegistry;
    }

    // ---- Issuance (issuer only) ----

    function mint(address to, uint256 value) external onlyIssuer {
        require(to != address(0), "tRWA: mint to zero");
        _requireEligible(to);
        totalSupply += value;
        balanceOf[to] += value;
        emit Mint(to, value);
        emit Transfer(address(0), to, value);
    }

    function burn(address from, uint256 value) external onlyIssuer {
        require(balanceOf[from] >= value, "tRWA: burn exceeds balance");
        balanceOf[from] -= value;
        totalSupply -= value;
        emit Burn(from, value);
        emit Transfer(from, address(0), value);
    }

    // ---- ERC-20 ----

    function transfer(address to, uint256 value) external returns (bool) {
        _transfer(msg.sender, to, value);
        return true;
    }

    function approve(address spender, uint256 value) external returns (bool) {
        allowance[msg.sender][spender] = value;
        emit Approval(msg.sender, spender, value);
        return true;
    }

    function transferFrom(address from, address to, uint256 value) external returns (bool) {
        uint256 allowed = allowance[from][msg.sender];
        require(allowed >= value, "tRWA: insufficient allowance");
        if (allowed != type(uint256).max) {
            allowance[from][msg.sender] = allowed - value;
        }
        _transfer(from, to, value);
        return true;
    }

    function _transfer(address from, address to, uint256 value) internal {
        require(to != address(0), "tRWA: transfer to zero");
        _requireEligible(to);
        require(balanceOf[from] >= value, "tRWA: insufficient balance");
        balanceOf[from] -= value;
        balanceOf[to] += value;
        emit Transfer(from, to, value);
    }

    // Requires the recipient to be eligible when a registry is configured. The
    // check is recipient-centric, in the spirit of ERC-3643: a holder was already
    // checked when it acquired the asset, so the sender is not re-checked here.
    function _requireEligible(address to) internal view {
        if (registry != address(0)) {
            require(IEligibilityRegistry(registry).isEligible(to), "tRWA: recipient not eligible");
        }
    }
}
