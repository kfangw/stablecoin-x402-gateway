// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface ISpendToken {
    function transfer(address to, uint256 value) external returns (bool);
    function transferFrom(address from, address to, uint256 value) external returns (bool);
}

/// @title DelegatedSpend
/// @notice On-chain enforcement of a delegated spending mandate. A delegator
///         deposits tKRW and registers a mandate (agent, per-payment cap,
///         validity window, payee allowlist, cumulative budget). The agent then
///         spends from the deposit through pay(), which enforces the same terms
///         the gateway's off-chain MandatePolicy enforces, in the same order.
///         Two differences from the gateway are intentional and covered by the
///         parity tests: resource scoping stays off chain, since the chain cannot
///         see URLs; and the cumulative budget uses a fixed window rather than a
///         sliding one, to avoid the gas cost of tracking per-payment timestamps.
///         This is a minimal contract for verifying the flow, not production code.
contract DelegatedSpend {
    address public immutable paymentToken;

    struct OnchainMandate {
        address delegator;
        address agent;
        uint256 maxAmountPerPayment;
        uint256 validAfter;
        uint256 validBefore;
        uint256 budgetAmount;
        uint256 budgetWindowSeconds;
        bool revoked;
        uint256 windowStart;
        uint256 windowSpent;
        address[] payees;
    }

    mapping(address => uint256) public deposits;
    mapping(bytes32 => OnchainMandate) private mandates;

    event Deposited(address indexed delegator, uint256 amount);
    event Withdrawn(address indexed delegator, uint256 amount);
    event MandateSet(bytes32 indexed id, address indexed delegator, address indexed agent);
    event Revoked(bytes32 indexed id);
    event Paid(bytes32 indexed id, address indexed to, uint256 amount);

    constructor(address paymentToken_) {
        paymentToken = paymentToken_;
    }

    /// @notice Deposits tKRW into the caller's balance. The caller must have
    ///         approved this contract for at least amount.
    function deposit(uint256 amount) external {
        require(
            ISpendToken(paymentToken).transferFrom(msg.sender, address(this), amount),
            "spend: deposit transfer failed"
        );
        deposits[msg.sender] += amount;
        emit Deposited(msg.sender, amount);
    }

    /// @notice Withdraws tKRW from the caller's deposit balance.
    function withdraw(uint256 amount) external {
        require(deposits[msg.sender] >= amount, "spend: insufficient deposit");
        deposits[msg.sender] -= amount;
        require(ISpendToken(paymentToken).transfer(msg.sender, amount), "spend: withdraw transfer failed");
        emit Withdrawn(msg.sender, amount);
    }

    /// @notice Registers or updates a mandate owned by the caller (the delegator).
    ///         Updating resets the cumulative-budget window.
    function setMandate(
        bytes32 id,
        address agent,
        uint256 maxAmountPerPayment,
        uint256 validAfter,
        uint256 validBefore,
        uint256 budgetAmount,
        uint256 budgetWindowSeconds,
        address[] calldata payees
    ) external {
        OnchainMandate storage m = mandates[id];
        if (m.delegator != address(0)) {
            require(m.delegator == msg.sender, "spend: not mandate delegator");
        }
        m.delegator = msg.sender;
        m.agent = agent;
        m.maxAmountPerPayment = maxAmountPerPayment;
        m.validAfter = validAfter;
        m.validBefore = validBefore;
        m.budgetAmount = budgetAmount;
        m.budgetWindowSeconds = budgetWindowSeconds;
        m.revoked = false;
        m.windowStart = block.timestamp;
        m.windowSpent = 0;
        m.payees = payees;
        emit MandateSet(id, msg.sender, agent);
    }

    /// @notice Revokes a mandate. Only its delegator may revoke.
    function revoke(bytes32 id) external {
        OnchainMandate storage m = mandates[id];
        require(m.delegator == msg.sender, "spend: not mandate delegator");
        m.revoked = true;
        emit Revoked(id);
    }

    /// @notice Spends amount from the mandate's deposit to a payee. Checks run
    ///         cheapest first, matching the gateway's MandatePolicy order.
    function pay(bytes32 id, address to, uint256 amount) external {
        OnchainMandate storage m = mandates[id];
        require(m.agent == msg.sender, "spend: caller is not the mandated agent");
        require(!m.revoked, "spend: mandate revoked");
        require(block.timestamp > m.validAfter, "spend: mandate not yet valid");
        require(block.timestamp < m.validBefore, "spend: mandate expired");
        require(_allowedPayee(m, to), "spend: payee not allowed");
        require(amount <= m.maxAmountPerPayment, "spend: over per-payment limit");

        // Fixed-window cumulative budget: once the window elapses it resets whole,
        // rather than sliding with each payment as the gateway's accounting does.
        if (m.budgetWindowSeconds > 0 && block.timestamp >= m.windowStart + m.budgetWindowSeconds) {
            m.windowStart = block.timestamp;
            m.windowSpent = 0;
        }
        require(m.windowSpent + amount <= m.budgetAmount, "spend: over cumulative budget");

        require(deposits[m.delegator] >= amount, "spend: insufficient deposit");
        m.windowSpent += amount;
        deposits[m.delegator] -= amount;
        require(ISpendToken(paymentToken).transfer(to, amount), "spend: payment transfer failed");
        emit Paid(id, to, amount);
    }

    function _allowedPayee(OnchainMandate storage m, address to) internal view returns (bool) {
        // An empty allowlist allows any payee, matching the off-chain default.
        if (m.payees.length == 0) {
            return true;
        }
        for (uint256 i = 0; i < m.payees.length; i++) {
            if (m.payees[i] == to) {
                return true;
            }
        }
        return false;
    }

    // ---- Views ----

    function mandateAgent(bytes32 id) external view returns (address) {
        return mandates[id].agent;
    }

    function mandateRevoked(bytes32 id) external view returns (bool) {
        return mandates[id].revoked;
    }

    function windowSpent(bytes32 id) external view returns (uint256) {
        return mandates[id].windowSpent;
    }
}
