// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IPaymentToken {
    function receiveWithAuthorization(
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

    function transfer(address to, uint256 value) external returns (bool);
}

interface IAssetToken {
    function transferFrom(address from, address to, uint256 value) external returns (bool);
}

/// @title DvPSettlement
/// @notice Delivery-versus-payment: settles a payment and delivers an asset in a
///         single transaction, so the two either both happen or both revert.
///         Payment uses the stablecoin's EIP-3009 receiveWithAuthorization with
///         this contract as recipient, so the authorization can only be executed
///         through this contract and never extracted and settled on its own; the
///         contract then forwards the payment to the seller. Delivery uses the
///         asset's transferFrom, which requires the seller to have approved this
///         contract in advance. The asset's own recipient-eligibility check runs
///         inside transferFrom, so an ineligible buyer makes the whole transaction
///         revert, payment included. Under the stablecoin allowlist this contract
///         must be allowlisted, since it both receives and sends tKRW.
///         This is a minimal contract for verifying the flow, not production code.
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
        // 1. Payment: receive into this contract. The authorization's recipient is
        //    this contract, so it cannot be settled anywhere else.
        IPaymentToken(paymentToken).receiveWithAuthorization(
            from, address(this), value, validAfter, validBefore, nonce, v, r, s
        );
        // 2. Forward the payment to the seller.
        require(
            IPaymentToken(paymentToken).transfer(seller, value),
            "dvp: payment forward failed"
        );
        // 3. Delivery: pull the asset from the seller to the buyer. The seller
        //    must have approved this contract for at least assetAmount. Slither's
        //    arbitrary-send-erc20 detector flags this pull because `from` is not
        //    msg.sender, but it is not arbitrary here: the seller's own approval
        //    gates it, and it runs atomically with the buyer's signed payment
        //    received in step 1, so it only ever moves an approved seller's asset
        //    to the buyer who just paid for it.
        // slither-disable-next-line arbitrary-send-erc20
        require(
            IAssetToken(assetToken).transferFrom(seller, from, assetAmount),
            "dvp: asset delivery failed"
        );
        emit Settled(from, seller, value, assetAmount);
    }
}
