// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

//go:build embedded

package xatu

import (
	"math/big"

	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"

	"github.com/erigontech/erigon/execution/chain"
	erigontypes "github.com/erigontech/erigon/execution/types"
)

// Compile-time interface checks.
var (
	_ execution.Block       = (*blockAdapter)(nil)
	_ execution.Transaction = (*transactionAdapter)(nil)
	_ execution.Receipt     = (*receiptAdapter)(nil)
)

// blockAdapter wraps an Erigon Block to implement execution.Block.
type blockAdapter struct {
	block *erigontypes.Block
	txs   []execution.Transaction
}

// newBlockAdapter creates a new blockAdapter from an Erigon Block.
func newBlockAdapter(block *erigontypes.Block, chainConfig *chain.Config) *blockAdapter {
	erigonTxs := block.Transactions()
	txs := make([]execution.Transaction, len(erigonTxs))

	signer := erigontypes.MakeSigner(chainConfig, block.NumberU64(), block.Time())

	for i, tx := range erigonTxs {
		txs[i] = newTransactionAdapter(tx, signer)
	}

	return &blockAdapter{
		block: block,
		txs:   txs,
	}
}

// Number returns the block number.
func (b *blockAdapter) Number() *big.Int {
	return b.block.Number()
}

// Hash returns the block hash.
func (b *blockAdapter) Hash() execution.Hash {
	return execution.Hash(b.block.Hash())
}

// ParentHash returns the parent block hash.
func (b *blockAdapter) ParentHash() execution.Hash {
	return execution.Hash(b.block.ParentHash())
}

// BaseFee returns the base fee per gas (EIP-1559), or nil for pre-London blocks.
func (b *blockAdapter) BaseFee() *big.Int {
	return b.block.BaseFee()
}

// Transactions returns all transactions in the block.
func (b *blockAdapter) Transactions() []execution.Transaction {
	return b.txs
}

// transactionAdapter wraps an Erigon Transaction to implement execution.Transaction.
type transactionAdapter struct {
	tx   erigontypes.Transaction
	from execution.Address
}

// newTransactionAdapter creates a new transactionAdapter from an Erigon Transaction.
func newTransactionAdapter(tx erigontypes.Transaction, signer *erigontypes.Signer) *transactionAdapter {
	from, _ := tx.Sender(*signer)

	return &transactionAdapter{
		tx:   tx,
		from: execution.Address(from.Value()),
	}
}

// Hash returns the transaction hash.
func (t *transactionAdapter) Hash() execution.Hash {
	return execution.Hash(t.tx.Hash())
}

// Type returns the transaction type.
func (t *transactionAdapter) Type() uint8 {
	return t.tx.Type()
}

// To returns the recipient address, or nil for contract creation.
func (t *transactionAdapter) To() *execution.Address {
	to := t.tx.GetTo()
	if to == nil {
		return nil
	}

	addr := execution.Address(*to)

	return &addr
}

// From returns the sender address.
func (t *transactionAdapter) From() execution.Address {
	return t.from
}

// Nonce returns the sender account nonce.
func (t *transactionAdapter) Nonce() uint64 {
	return t.tx.GetNonce()
}

// Gas returns the gas limit.
func (t *transactionAdapter) Gas() uint64 {
	return t.tx.GetGasLimit()
}

// GasPrice returns the gas price (for legacy transactions).
func (t *transactionAdapter) GasPrice() *big.Int {
	// For legacy transactions, GetFeeCap returns the gas price
	feeCap := t.tx.GetFeeCap()
	if feeCap == nil {
		return nil
	}

	return feeCap.ToBig()
}

// GasTipCap returns the max priority fee per gas (EIP-1559).
func (t *transactionAdapter) GasTipCap() *big.Int {
	tip := t.tx.GetTipCap()
	if tip == nil {
		return nil
	}

	return tip.ToBig()
}

// GasFeeCap returns the max fee per gas (EIP-1559).
func (t *transactionAdapter) GasFeeCap() *big.Int {
	feeCap := t.tx.GetFeeCap()
	if feeCap == nil {
		return nil
	}

	return feeCap.ToBig()
}

// Value returns the value transferred in wei.
func (t *transactionAdapter) Value() *big.Int {
	value := t.tx.GetValue()

	return value.ToBig()
}

// Data returns the input data (calldata).
func (t *transactionAdapter) Data() []byte {
	return t.tx.GetData()
}

// Size returns the encoded transaction size in bytes.
func (t *transactionAdapter) Size() uint64 {
	return uint64(t.tx.EncodingSize())
}

// ChainId returns the chain ID, or nil for legacy transactions.
func (t *transactionAdapter) ChainId() *big.Int {
	chainID := t.tx.GetChainID()
	if chainID == nil {
		return nil
	}

	return chainID.ToBig()
}

// BlobGas returns the blob gas used (for blob transactions).
func (t *transactionAdapter) BlobGas() uint64 {
	return t.tx.GetBlobGas()
}

// BlobGasFeeCap returns the max blob fee per gas (for blob transactions).
func (t *transactionAdapter) BlobGasFeeCap() *big.Int {
	// BlobTx is the only transaction type with MaxFeePerBlobGas
	if blobTx, ok := t.tx.(*erigontypes.BlobTx); ok {
		if blobTx.MaxFeePerBlobGas != nil {
			return blobTx.MaxFeePerBlobGas.ToBig()
		}
	}

	return nil
}

// BlobHashes returns the versioned hashes (for blob transactions).
func (t *transactionAdapter) BlobHashes() []execution.Hash {
	erigonHashes := t.tx.GetBlobHashes()
	hashes := make([]execution.Hash, len(erigonHashes))

	for i, h := range erigonHashes {
		hashes[i] = execution.Hash(h)
	}

	return hashes
}

// receiptAdapter wraps an Erigon Receipt to implement execution.Receipt.
type receiptAdapter struct {
	receipt *erigontypes.Receipt
}

// newReceiptAdapter creates a new receiptAdapter from an Erigon Receipt.
func newReceiptAdapter(receipt *erigontypes.Receipt) *receiptAdapter {
	return &receiptAdapter{receipt: receipt}
}

// Status returns the transaction status (1=success, 0=failure).
func (r *receiptAdapter) Status() uint64 {
	return r.receipt.Status
}

// TxHash returns the transaction hash.
func (r *receiptAdapter) TxHash() execution.Hash {
	return execution.Hash(r.receipt.TxHash)
}

// GasUsed returns the gas used by the transaction.
func (r *receiptAdapter) GasUsed() uint64 {
	return r.receipt.GasUsed
}

// adaptReceipts converts a slice of Erigon receipts to execution.Receipt interfaces.
func adaptReceipts(receipts erigontypes.Receipts) []execution.Receipt {
	result := make([]execution.Receipt, len(receipts))

	for i, r := range receipts {
		result[i] = newReceiptAdapter(r)
	}

	return result
}
