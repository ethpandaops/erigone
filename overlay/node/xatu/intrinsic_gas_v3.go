//go:build embedded && !erigon_main

package xatu

import (
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/fixedgas"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/vm"
)

// calcIntrinsicGasForTx calculates intrinsic gas for a transaction, optionally
// applying custom gas schedule overrides. Uses fixedgas.IntrinsicGas (v3 branch).
func calcIntrinsicGasForTx(txn erigontypes.Transaction, chainRules *chain.Rules, gasSchedule *CustomGasSchedule) uint64 {
	accessList := txn.GetAccessList()
	var accessListLen, storageKeysLen uint64
	if accessList != nil {
		accessListLen = uint64(len(accessList))
		storageKeysLen = uint64(accessList.StorageKeys())
	}

	intrinsicGas, _, _ := fixedgas.IntrinsicGas(
		txn.GetData(), accessListLen, storageKeysLen,
		txn.GetTo() == nil, chainRules.IsHomestead, chainRules.IsIstanbul,
		chainRules.IsShanghai, chainRules.IsPrague, false, 0,
	)

	if gasSchedule != nil {
		vmSchedule := gasSchedule.ToVMGasSchedule()
		if vmSchedule != nil && vmSchedule.HasIntrinsicOverrides() {
			intrinsicGas, _ = vm.CalcCustomIntrinsicGas(
				vmSchedule, txn.GetData(), accessListLen, storageKeysLen,
				txn.GetTo() == nil, chainRules.IsHomestead, chainRules.IsIstanbul,
				chainRules.IsShanghai, chainRules.IsPrague, false, 0,
			)
		}
	}

	return intrinsicGas
}
