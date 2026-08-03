// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title KRWTestStablecoin (tKRW)
/// @notice A demo KRW stablecoin. It models an issuance and distribution flow in which
///         the issuer mints and burns against off-chain reserves. This is a minimal
///         contract for verifying the flow, not a production token.
contract KRWTestStablecoin {
    string public constant name = "KRW Test Stablecoin";
    string public constant symbol = "tKRW";
    uint8 public constant decimals = 0; // 1 tKRW = 1 KRW, simplified for the demo

    address public issuer;
    bool public paused;

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);
    event Mint(address indexed to, uint256 value);
    event Burn(address indexed from, uint256 value);
    event PauseSet(bool paused);

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
}
