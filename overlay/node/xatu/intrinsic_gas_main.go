//go:build embedded && erigon_main

package xatu

import (
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/mdgas"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/vm"
)

// calcIntrinsicGasForTx calculates intrinsic gas for a transaction, optionally
// applying custom gas schedule overrides. Uses mdgas.IntrinsicGas (main branch).
func calcIntrinsicGasForTx(txn erigontypes.Transaction, chainRules *chain.Rules, gasSchedule *CustomGasSchedule) uint64 {
	accessList := txn.GetAccessList()
	var accessListLen, storageKeysLen uint64
	if accessList != nil {
		accessListLen = uint64(len(accessList))
		storageKeysLen = uint64(accessList.StorageKeys())
	}

	intrinsicGasResult, _ := mdgas.IntrinsicGas(mdgas.IntrinsicGasCalcArgs{
		Data:               txn.GetData(),
		AccessListLen:      accessListLen,
		StorageKeysLen:     storageKeysLen,
		IsContractCreation: txn.GetTo() == nil,
		IsEIP2:             chainRules.IsHomestead,
		IsEIP2028:          chainRules.IsIstanbul,
		IsEIP3860:          chainRules.IsShanghai,
		IsEIP7623:          chainRules.IsPrague,
	})
	intrinsicGas := intrinsicGasResult.RegularGas

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
