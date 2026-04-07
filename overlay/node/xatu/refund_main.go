//go:build embedded && erigon_main

package xatu

import "github.com/erigontech/erigon/execution/tracing"

// getRefundValue extracts the refund as uint64 from IntraBlockState.
// On main, GetRefund() returns mdgas.MdGas so we access .Regular.
func getRefundValue(ibs tracing.IntraBlockState) uint64 {
	return ibs.GetRefund().Regular
}
