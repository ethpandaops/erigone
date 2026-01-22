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

package xatu

import (
	"encoding/hex"

	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"

	"github.com/erigontech/erigon/common/math"
	"github.com/erigontech/erigon/execution/tracing"
	"github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
)

// StructLogConfig configures the structlog tracer.
type StructLogConfig struct {
	DisableMemory    bool
	DisableStack     bool
	DisableStorage   bool
	EnableReturnData bool
}

// StructLogTracer captures structlog traces for execution-processor.
type StructLogTracer struct {
	cfg        StructLogConfig
	logs       []execution.StructLog
	output     []byte
	err        error
	env        *tracing.VMContext
	gasUsed    uint64
	returnData []byte
}

// NewStructLogTracer creates a new structlog tracer.
func NewStructLogTracer(cfg StructLogConfig) *StructLogTracer {
	return &StructLogTracer{
		cfg:  cfg,
		logs: make([]execution.StructLog, 0, 256),
	}
}

// Hooks returns the tracing hooks for the EVM.
func (t *StructLogTracer) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: t.OnTxStart,
		OnTxEnd:   t.OnTxEnd,
		OnExit:    t.OnExit,
		OnOpcode:  t.OnOpcode,
	}
}

// OnTxStart is called when a transaction starts.
func (t *StructLogTracer) OnTxStart(env *tracing.VMContext, _ types.Transaction, _ accounts.Address) {
	t.env = env
}

// OnTxEnd is called when a transaction ends.
func (t *StructLogTracer) OnTxEnd(receipt *types.Receipt, err error) {
	if err != nil {
		if t.err == nil {
			t.err = err
		}

		return
	}

	t.gasUsed = receipt.GasUsed
}

// OnOpcode captures each EVM opcode execution.
func (t *StructLogTracer) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	op := vm.OpCode(opcode)
	stack := scope.StackData()

	log := execution.StructLog{
		PC:      uint32(pc),
		Op:      op.String(),
		Gas:     gas,
		GasCost: cost,
		Depth:   uint64(depth),
	}

	// Capture stack if enabled
	if !t.cfg.DisableStack && len(stack) > 0 {
		stackStrs := make([]string, len(stack))
		for i, item := range stack {
			stackStrs[i] = hex.EncodeToString(math.PaddedBigBytes(item.ToBig(), 32))
		}

		log.Stack = &stackStrs
	}

	// Capture return data if enabled
	if t.cfg.EnableReturnData && len(rData) > 0 {
		returnData := hex.EncodeToString(rData)
		log.ReturnData = &returnData
	}

	// Capture refund
	if t.env != nil {
		refund := t.env.IntraBlockState.GetRefund()
		log.Refund = &refund
	}

	// Capture error
	if err != nil {
		errStr := err.Error()
		log.Error = &errStr
	}

	t.logs = append(t.logs, log)
}

// OnExit is called when execution exits.
func (t *StructLogTracer) OnExit(depth int, output []byte, _ uint64, err error, _ bool) {
	if depth != 0 {
		return
	}

	t.output = make([]byte, len(output))
	copy(t.output, output)
	t.err = err
}

// GetTraceTransaction returns the trace result in execution-processor format.
func (t *StructLogTracer) GetTraceTransaction() *execution.TraceTransaction {
	trace := &execution.TraceTransaction{
		Gas:        t.gasUsed,
		Failed:     t.err != nil,
		Structlogs: t.logs,
	}

	if len(t.output) > 0 {
		returnValue := hex.EncodeToString(t.output)
		trace.ReturnValue = &returnValue
	}

	return trace
}

// StructLogs returns the captured log entries.
func (t *StructLogTracer) StructLogs() []execution.StructLog {
	return t.logs
}

// Error returns the VM error captured by the trace.
func (t *StructLogTracer) Error() error {
	return t.err
}

// Output returns the VM return value captured by the trace.
func (t *StructLogTracer) Output() []byte {
	return t.output
}
