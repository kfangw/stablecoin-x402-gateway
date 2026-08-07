// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IdentityRegistry
/// @notice A minimal agent identity registry in the spirit of ERC-8004: it maps
///         an agent address to a registration flag and an agent-card URL. Agents
///         self-register with msg.sender, matching the registration model of the
///         deployed registries this stands in for. The card URL is stored as an
///         opaque string; fetching or validating it is out of scope here.
///         This is a minimal contract for verifying the flow, not production code.
contract IdentityRegistry {
    mapping(address => string) private cards;
    mapping(address => bool) private registered;

    event AgentRegistered(address indexed agent, string agentCardURL);

    /// @notice Registers the caller with an agent-card URL. Calling again from
    ///         the same address updates the stored URL.
    function register(string calldata agentCardURL) external {
        registered[msg.sender] = true;
        cards[msg.sender] = agentCardURL;
        emit AgentRegistered(msg.sender, agentCardURL);
    }

    /// @notice Reports whether an address has registered.
    function isRegistered(address agent) external view returns (bool) {
        return registered[agent];
    }

    /// @notice Returns the agent-card URL stored for an address, or the empty
    ///         string if it has not registered.
    function agentCard(address agent) external view returns (string memory) {
        return cards[agent];
    }
}
