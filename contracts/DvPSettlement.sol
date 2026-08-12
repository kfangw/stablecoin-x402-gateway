// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IPaymentToken {
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
    ) external;
}

interface IAssetToken {
    function transferFrom(address from, address to, uint256 value) external returns (bool);
}

/// @title DvPSettlement
/// @notice Delivery-versus-payment: settles a payment and delivers an asset in a
///         single transaction, so the two either both happen or both revert.
///         Payment uses the stablecoin's EIP-3009 transferWithAuthorization with
///         the seller as recipient; delivery uses the asset's transferFrom, which
///         requires the seller to have approved this contract in advance. The
///         asset's own recipient-eligibility check runs inside transferFrom, so an
///         ineligible buyer makes the whole transaction revert, payment included.
///         This is a minimal contract for verifying the flow, not production code.
///         The payment authorization is not bound to this contract; a future
///         receive-style path closes the window where the authorization could be
///         extracted and submitted on its own.
contract DvPSettlement {
    address public immutable paymentToken;
    address public immutable assetToken;

    event Settled(
        address indexed buyer,
        address indexed seller,
        uint256 paymentValue,
        uint256 assetAmount
    );

    constructor(address paymentToken_, address assetToken_) {
        paymentToken = paymentToken_;
        assetToken = assetToken_;
    }

    /// @notice Settles payment from the buyer to the seller and delivers the
    ///         asset from the seller to the buyer, atomically.
    function settleAndDeliver(
        address seller,
        uint256 assetAmount,
        address from,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external {
        // 1. Payment: the authorization's recipient must be the seller.
        IPaymentToken(paymentToken).transferWithAuthorization(
            from, seller, value, validAfter, validBefore, nonce, v, r, s
        );
        // 2. Delivery: pull the asset from the seller to the buyer. The seller
        //    must have approved this contract for at least assetAmount.
        require(
            IAssetToken(assetToken).transferFrom(seller, from, assetAmount),
            "dvp: asset delivery failed"
        );
        emit Settled(from, seller, value, assetAmount);
    }
}
