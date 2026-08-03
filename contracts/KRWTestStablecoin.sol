// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title KRWTestStablecoin (tKRW)
/// @notice The ERC-20 skeleton of a demo KRW stablecoin.
contract KRWTestStablecoin {
    string public constant name = "KRW Test Stablecoin";
    string public constant symbol = "tKRW";
    uint8 public constant decimals = 0; // 1 tKRW = 1 KRW, simplified for the demo

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

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

    function transferFrom(address from, address to, uint256 value)
        external
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
