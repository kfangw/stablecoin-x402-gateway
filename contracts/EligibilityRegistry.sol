// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title EligibilityRegistry
/// @notice A minimal recipient-eligibility registry in the spirit of ERC-3643.
///         A registrar marks accounts eligible directly. Separately, any account
///         can sponsor an agent so the agent inherits its delegator's eligibility.
///         Inheritance is computed dynamically: isEligible(a) is true when a is
///         eligible itself or when the account that sponsors a is eligible.
///         Because the inherited flag is read through the delegator rather than
///         copied, turning off a delegator's eligibility immediately removes the
///         agent's inherited eligibility, with no revocation to propagate.
///         This is a minimal contract for verifying the flow, not production code.
contract EligibilityRegistry {
    address public registrar;

    mapping(address => bool) public eligible;
    // delegatedBy[agent] is the account that sponsors agent, or zero if none.
    mapping(address => address) public delegatedBy;

    event EligibilitySet(address indexed account, bool eligible);
    event EligibilityDelegated(address indexed delegator, address indexed agent);

    modifier onlyRegistrar() {
        require(msg.sender == registrar, "eligibility: not registrar");
        _;
    }

    constructor() {
        registrar = msg.sender;
    }

    /// @notice Marks an account eligible or not. Registrar only.
    function setEligible(address account, bool value) external onlyRegistrar {
        eligible[account] = value;
        emit EligibilitySet(account, value);
    }

    /// @notice Sponsors an agent so it inherits the caller's eligibility. The
    ///         caller sponsors a free agent; the current sponsor calls again to
    ///         withdraw, clearing the delegation to zero. A different account
    ///         cannot overwrite an existing sponsorship.
    function delegateEligibility(address agent) external {
        address current = delegatedBy[agent];
        if (current == msg.sender) {
            delegatedBy[agent] = address(0);
            emit EligibilityDelegated(address(0), agent);
        } else {
            require(current == address(0), "eligibility: agent already delegated");
            delegatedBy[agent] = msg.sender;
            emit EligibilityDelegated(msg.sender, agent);
        }
    }

    /// @notice Reports whether an account is eligible, directly or by inheriting
    ///         from the account that sponsors it.
    function isEligible(address account) external view returns (bool) {
        if (eligible[account]) {
            return true;
        }
        address delegator = delegatedBy[account];
        return delegator != address(0) && eligible[delegator];
    }
}
