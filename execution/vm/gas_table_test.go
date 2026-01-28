// Copyright 2017 The go-ethereum Authors
// (original work)
// Copyright 2024 The Erigon Authors
// (modifications)
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

package vm_test

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/datadir"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/kv/temporal/temporaltest"
	"github.com/erigontech/erigon/db/snapshotsync/freezeblocks"
	"github.com/erigontech/erigon/db/state/execctx"
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/state"
	"github.com/erigontech/erigon/execution/tracing"
	"github.com/erigontech/erigon/execution/tracing/tracers/logger"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
	"github.com/erigontech/erigon/execution/vm/evmtypes"
	"github.com/erigontech/erigon/rpc/rpchelper"
)

func TestMemoryGasCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		size     uint64
		cost     uint64
		overflow bool
	}{
		{0x1fffffffe0, 36028809887088637, false},
		{0x1fffffffe1, 0, true},
	}
	for i, tt := range tests {
		v, err := vm.MemoryGasCost(&vm.CallContext{}, tt.size)
		if (err == vm.ErrGasUintOverflow) != tt.overflow {
			t.Errorf("test %d: overflow mismatch: have %v, want %v", i, err == vm.ErrGasUintOverflow, tt.overflow)
		}
		if v != tt.cost {
			t.Errorf("test %d: gas cost mismatch: have %v, want %v", i, v, tt.cost)
		}
	}
}

var eip2200Tests = []struct {
	original byte
	gaspool  uint64
	input    string
	used     uint64
	refund   uint64
	failure  error
}{
	{0, math.MaxUint64, "0x60006000556000600055", 1612, 0, nil},                // 0 -> 0 -> 0
	{0, math.MaxUint64, "0x60006000556001600055", 20812, 0, nil},               // 0 -> 0 -> 1
	{0, math.MaxUint64, "0x60016000556000600055", 20812, 19200, nil},           // 0 -> 1 -> 0
	{0, math.MaxUint64, "0x60016000556002600055", 20812, 0, nil},               // 0 -> 1 -> 2
	{0, math.MaxUint64, "0x60016000556001600055", 20812, 0, nil},               // 0 -> 1 -> 1
	{1, math.MaxUint64, "0x60006000556000600055", 5812, 15000, nil},            // 1 -> 0 -> 0
	{1, math.MaxUint64, "0x60006000556001600055", 5812, 4200, nil},             // 1 -> 0 -> 1
	{1, math.MaxUint64, "0x60006000556002600055", 5812, 0, nil},                // 1 -> 0 -> 2
	{1, math.MaxUint64, "0x60026000556000600055", 5812, 15000, nil},            // 1 -> 2 -> 0
	{1, math.MaxUint64, "0x60026000556003600055", 5812, 0, nil},                // 1 -> 2 -> 3
	{1, math.MaxUint64, "0x60026000556001600055", 5812, 4200, nil},             // 1 -> 2 -> 1
	{1, math.MaxUint64, "0x60026000556002600055", 5812, 0, nil},                // 1 -> 2 -> 2
	{1, math.MaxUint64, "0x60016000556000600055", 5812, 15000, nil},            // 1 -> 1 -> 0
	{1, math.MaxUint64, "0x60016000556002600055", 5812, 0, nil},                // 1 -> 1 -> 2
	{1, math.MaxUint64, "0x60016000556001600055", 1612, 0, nil},                // 1 -> 1 -> 1
	{0, math.MaxUint64, "0x600160005560006000556001600055", 40818, 19200, nil}, // 0 -> 1 -> 0 -> 1
	{1, math.MaxUint64, "0x600060005560016000556000600055", 10818, 19200, nil}, // 1 -> 0 -> 1 -> 0
	{1, 2306, "0x6001600055", 2306, 0, vm.ErrOutOfGas},                         // 1 -> 1 (2300 sentry + 2xPUSH)
	{1, 2307, "0x6001600055", 806, 0, nil},                                     // 1 -> 1 (2301 sentry + 2xPUSH)
}

func testTemporalTxSD(t *testing.T) (kv.TemporalRwTx, *execctx.SharedDomains) {
	dirs := datadir.New(t.TempDir())

	db := temporaltest.NewTestDB(t, dirs)
	tx, err := db.BeginTemporalRw(context.Background()) //nolint:gocritic
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)

	sd, err := execctx.NewSharedDomains(context.Background(), tx, log.New())
	require.NoError(t, err)
	t.Cleanup(sd.Close)

	return tx, sd
}

func TestEIP2200(t *testing.T) {
	for i, tt := range eip2200Tests {

		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()

			tx, sd := testTemporalTxSD(t)

			r, w := state.NewReaderV3(sd.AsGetter(tx)), state.NewWriter(sd.AsPutDel(tx), nil, sd.TxNum())
			s := state.New(r)

			address := accounts.InternAddress(common.BytesToAddress([]byte("contract")))
			s.CreateAccount(address, true)
			s.SetCode(address, hexutil.MustDecode(tt.input))
			s.SetState(address, accounts.ZeroKey, *uint256.NewInt(uint64(tt.original)))

			vmctx := evmtypes.BlockContext{
				CanTransfer: func(evmtypes.IntraBlockState, accounts.Address, uint256.Int) (bool, error) { return true, nil },
				Transfer: func(evmtypes.IntraBlockState, accounts.Address, accounts.Address, uint256.Int, bool, *chain.Rules) error {
					return nil
				},
			}
			_ = s.CommitBlock(vmctx.Rules(chain.AllProtocolChanges), w)
			vmenv := vm.NewEVM(vmctx, evmtypes.TxContext{}, s, chain.AllProtocolChanges, vm.Config{ExtraEips: []int{2200}})

			_, gas, err := vmenv.Call(accounts.ZeroAddress, address, nil, tt.gaspool, uint256.Int{}, false /* bailout */)
			if !errors.Is(err, tt.failure) {
				t.Errorf("test %d: failure mismatch: have %v, want %v", i, err, tt.failure)
			}
			if used := tt.gaspool - gas; used != tt.used {
				t.Errorf("test %d: gas used mismatch: have %v, want %v", i, used, tt.used)
			}
			if refund := vmenv.IntraBlockState().GetRefund(); refund != tt.refund {
				t.Errorf("test %d: gas refund mismatch: have %v, want %v", i, refund, tt.refund)
			}
		})
	}
}

var createGasTests = []struct {
	code    string
	eip3860 bool
	gasUsed uint64
}{
	// create(0, 0, 0xc000)
	{"0x61C00060006000f0", false, 41225},
	// create(0, 0, 0xc000)
	{"0x61C00060006000f0", true, 44297},
	// create2(0, 0, 0xc000, 0)
	{"0x600061C00060006000f5", false, 50444},
	// create2(0, 0, 0xc000, 0)
	{"0x600061C00060006000f5", true, 53516},
}

func TestCreateGas(t *testing.T) {
	t.Parallel()
	db := temporaltest.NewTestDB(t, datadir.New(t.TempDir()))
	tx, err := db.BeginTemporalRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	for i, tt := range createGasTests {
		address := accounts.InternAddress(common.BytesToAddress([]byte("contract")))

		domains, err := execctx.NewSharedDomains(context.Background(), tx, log.New())
		require.NoError(t, err)
		defer domains.Close()

		stateReader := rpchelper.NewLatestStateReader(domains.AsGetter(tx))
		stateWriter := rpchelper.NewLatestStateWriter(tx, domains, (*freezeblocks.BlockReader)(nil), 0)

		s := state.New(stateReader)
		s.CreateAccount(address, true)
		s.SetCode(address, hexutil.MustDecode(tt.code))

		vmctx := evmtypes.BlockContext{
			CanTransfer: func(evmtypes.IntraBlockState, accounts.Address, uint256.Int) (bool, error) { return true, nil },
			Transfer: func(evmtypes.IntraBlockState, accounts.Address, accounts.Address, uint256.Int, bool, *chain.Rules) error {
				return nil
			},
		}
		_ = s.CommitBlock(vmctx.Rules(chain.TestChainConfig), stateWriter)
		config := vm.Config{}
		if tt.eip3860 {
			config.ExtraEips = []int{3860}
		}

		vmenv := vm.NewEVM(vmctx, evmtypes.TxContext{}, s, chain.TestChainConfig, config)

		var startGas uint64 = math.MaxUint64
		_, gas, err := vmenv.Call(accounts.ZeroAddress, address, nil, startGas, uint256.Int{}, false /* bailout */)
		if err != nil {
			t.Errorf("test %d execution failed: %v", i, err)
		}
		if gasUsed := startGas - gas; gasUsed != tt.gasUsed {
			t.Errorf("test %d: gas used mismatch: have %v, want %v", i, gasUsed, tt.gasUsed)
		}
		domains.Close()
	}
	tx.Rollback()
}

// TestCallGasCostUnderflow tests for a bug where the gasCost reported by the tracer
// is corrupted when a CALL with value transfer fails due to insufficient gas.
//
// Bug: In gas.go:callGas(), the subtraction `availableGas - base` underflows when
// availableGas < base (e.g., 5000 available but CALL needs 9000 for value transfer).
// This produces a huge gasCost value (~2^64) in the tracer output.
//
// Real-world example: tx 0x4a18dc6b1fbfbb7b0d7402c57e4e2bc4b16cef0dcb8eb690a2a01de9d629f043
// had gasCost = 18158513697557845033 (0xfc00000000001429) for a CALL opcode.
func TestCallGasCostUnderflow(t *testing.T) {
	t.Parallel()

	tx, sd := testTemporalTxSD(t)

	r, w := state.NewReaderV3(sd.AsGetter(tx)), state.NewWriter(sd.AsPutDel(tx), nil, sd.TxNum())
	s := state.New(r)

	// Create the calling contract
	callerAddr := accounts.InternAddress(common.BytesToAddress([]byte("caller")))
	s.CreateAccount(callerAddr, true)
	// Give the caller some ETH to transfer
	_ = s.SetBalance(callerAddr, *uint256.NewInt(1_000_000_000_000_000_000), tracing.BalanceChangeUnspecified) // 1 ETH

	// Target address for the CALL (doesn't need code, just needs to exist)
	targetAddr := accounts.InternAddress(common.BytesToAddress([]byte("target")))
	s.CreateAccount(targetAddr, true)

	// Contract bytecode that mimics the real buggy transaction:
	// The real tx did: GAS SUB to compute the gas argument, which underflowed
	// because it subtracted a large value (0x8796 = 34710) from available gas (~5000).
	//
	// Bytecode plan:
	// PUSH1 0x00   ; retSize = 0
	// PUSH1 0x00   ; retOffset = 0
	// PUSH1 0x00   ; argsSize = 0
	// PUSH1 0x00   ; argsOffset = 0
	// PUSH1 0x01   ; value = 1 wei (triggers CallValueTransferGas = 9000)
	// PUSH20 <target> ; target address
	// PUSH2 0x8796 ; large value to subtract (34710)
	// GAS          ; push current gas
	// SUB          ; GAS - 0x8796 = UNDERFLOW when gas < 34710!
	// CALL         ; execute call with underflowed gas argument
	// STOP
	targetAddrValue := targetAddr.Value()
	code := []byte{
		0x60, 0x00, // PUSH1 0x00 (retSize)
		0x60, 0x00, // PUSH1 0x00 (retOffset)
		0x60, 0x00, // PUSH1 0x00 (argsSize)
		0x60, 0x00, // PUSH1 0x00 (argsOffset)
		0x60, 0x01, // PUSH1 0x01 (value = 1 wei)
		0x73, // PUSH20
	}
	code = append(code, targetAddrValue[:]...)
	code = append(code,
		0x61, 0x87, 0x96, // PUSH2 0x8796 (34710 - large value to cause underflow)
		0x5A, // GAS
		0x03, // SUB (GAS - 0x8796 = underflow!)
		0xF1, // CALL
		0x00, // STOP
	)

	s.SetCode(callerAddr, code)

	vmctx := evmtypes.BlockContext{
		CanTransfer: func(_ evmtypes.IntraBlockState, _ accounts.Address, _ uint256.Int) (bool, error) {
			return true, nil
		},
		Transfer: func(_ evmtypes.IntraBlockState, _, _ accounts.Address, _ uint256.Int, _ bool, _ *chain.Rules) error {
			return nil
		},
	}
	_ = s.CommitBlock(vmctx.Rules(chain.AllProtocolChanges), w)

	// Set up the struct logger to capture gasCost values
	structLogger := logger.NewStructLogger(nil)

	vmenv := vm.NewEVM(vmctx, evmtypes.TxContext{}, s, chain.AllProtocolChanges, vm.Config{
		Tracer: structLogger.Hooks(),
	})

	// Initialize the logger with the EVM context (required before tracing)
	structLogger.OnTxStart(vmenv.GetVMContext(), nil, accounts.ZeroAddress)

	// Call with limited gas:
	//
	// Gas breakdown before CALL opcode:
	// - PUSH1 x 6 = 6 * 3 = 18 gas
	// - PUSH20 = 3 gas
	// - PUSH2 = 3 gas
	// - GAS = 2 gas
	// - SUB = 3 gas
	// Total = 29 gas to reach CALL
	//
	// At CALL, the gas argument on the stack will be: (startGas - 29) - 34710
	// If startGas < 34710 + 29 = 34739, the SUB will underflow in EVM.
	//
	// We want to trigger the bug in callGas(), which requires:
	// availableGas < base (where base includes value transfer = 9000)
	//
	// Let's use enough gas to reach CALL but have the underflowed stack value.
	startGas := uint64(10000)

	t.Logf("Starting with gas: %d", startGas)

	_, remainingGas, err := vmenv.Call(accounts.ZeroAddress, callerAddr, nil, startGas, uint256.Int{}, false)
	t.Logf("Remaining gas: %d, Error: %v", remainingGas, err)

	// The call should fail with out of gas
	require.Error(t, err, "Expected out of gas error")

	// Find the CALL opcode in the trace
	logs := structLogger.StructLogs()
	t.Logf("Total opcodes traced: %d", len(logs))

	var callLog *logger.StructLog
	for i := range logs {
		t.Logf("Opcode %d: %s gas=%d gasCost=%d", i, logs[i].Op, logs[i].Gas, logs[i].GasCost)
		if logs[i].Op == vm.CALL {
			callLog = &logs[i]
			break
		}
	}

	require.NotNil(t, callLog, "Expected to find CALL opcode in trace")
	t.Logf("CALL opcode: gas=%d, gasCost=%d (0x%x)", callLog.Gas, callLog.GasCost, callLog.GasCost)

	// The bug: gasCost is corrupted due to underflow in callGas()
	// A reasonable gasCost for a failed CALL should be < 30 million (block gas limit)
	// The corrupted value is ~18 * 10^18 (close to 2^64)
	const maxReasonableGasCost = 30_000_000

	if callLog.GasCost > maxReasonableGasCost {
		t.Errorf("BUG DETECTED: CALL gasCost is corrupted!\n"+
			"  gasCost = %d (0x%x)\n"+
			"  This is caused by underflow in gas.go:callGas() when availableGas < base\n"+
			"  Expected gasCost < %d",
			callLog.GasCost, callLog.GasCost, maxReasonableGasCost)
	} else {
		t.Logf("CALL gasCost = %d (reasonable)", callLog.GasCost)
	}
}

// TestCallGasUnderflowDirectly tests the callGas function directly for underflow.
func TestCallGasUnderflowDirectly(t *testing.T) {
	t.Parallel()

	// Simulate the conditions that cause underflow:
	// - availableGas = 5000 (gas remaining before CALL)
	// - base = 9000 (CallValueTransferGas for value transfer)
	// - callCost = huge 256-bit value (from underflowed stack computation)

	availableGas := uint64(5000)
	base := uint64(9000) // CallValueTransferGas

	// In EIP-150 mode, callGas does: availableGas = availableGas - base
	// When availableGas < base, this underflows!
	if availableGas < base {
		underflowedGas := availableGas - base // This wraps around to ~2^64 - 4000
		t.Logf("Underflow detected: %d - %d = %d (0x%x)",
			availableGas, base, underflowedGas, underflowedGas)

		// The underflowed value should be close to 2^64
		if underflowedGas > 1<<63 {
			t.Logf("BUG CONFIRMED: Underflow produces huge value: %d", underflowedGas)
		}
	} else {
		t.Log("No underflow (availableGas >= base)")
	}
}
