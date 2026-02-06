# Erigone

Erigone is a patched version of Erigon with custom extensions for ethPandaOps tooling.

## Architecture

Erigone uses an **overlay/patch architecture** to maintain modifications on top of upstream Erigon:

```
erigone/
├── overlay/           # New files copied into Erigon (no conflicts)
│   ├── execution/vm/  # VM extensions (GasSchedule)
│   └── node/xatu/     # Xatu service (simulation, tracing)
├── patches/           # Diffs applied to Erigon source
│   └── erigontech/erigon/main.patch
├── scripts/           # Build and patch management
└── erigon/            # Cloned Erigon with patches applied (gitignored)
```

## Build Workflow

```bash
# Full build (clones Erigon, applies patches, copies overlays, builds)
./scripts/erigone-build.sh -r erigontech/erigon -b main

# Skip build (for development - just apply patches)
./scripts/erigone-build.sh -r erigontech/erigon -b main --skip-build

# Then build manually
cd erigon && go build -tags "embedded,nosqlite,noboltdb,nosilkworm" ./...
```

## Development Workflow

**To modify patched files:**

1. Build first to get fresh `erigon/` clone with patches applied
2. Edit files in `erigon/` directory
3. Run `./scripts/save-patch.sh` to regenerate patch
4. Verify: `rm -rf erigon && ./scripts/erigone-build.sh ...`

**To modify overlay files:**

1. Edit files directly in `overlay/` directory
2. They get copied during build automatically

**IMPORTANT:** Never edit `patches/*.patch` files directly. Always edit the cloned `erigon/` source and regenerate.

## Gas Simulation System

The gas simulation feature allows re-executing transactions with custom gas costs to analyze "what if" scenarios (e.g., repricing proposals).

### How It Works

1. **Request** comes in via RPC (`xatu_simulateBlockGas` or `xatu_simulateTransactionGas`)
2. **Dual execution** runs each transaction twice:
   - Original: standard gas costs
   - Simulated: custom gas schedule
3. **Results** show gas comparison and per-opcode breakdown

### Key Components

**Overlay Files (new code):**

| File | Purpose |
|------|---------|
| `overlay/execution/vm/gas_schedule.go` | `GasSchedule` struct with `GetOr()` method |
| `overlay/node/xatu/custom_gas.go` | `CustomGasSchedule` type, key constants, `GasScheduleForRules()` |
| `overlay/node/xatu/jump_table.go` | `BuildCustomJumpTable()` for constant gas overrides |
| `overlay/node/xatu/simulation_rpc.go` | RPC handlers, dual execution logic |
| `overlay/node/xatu/simulation_tracer.go` | Per-opcode gas tracking |

**Patched Files (modifications to Erigon):**

| File | Changes |
|------|---------|
| `execution/vm/evm.go` | Added `GasSchedule *GasSchedule` field to EVM struct |
| `execution/vm/gas_table.go` | Dynamic gas functions use `evm.GasSchedule.GetOr()` |
| `execution/vm/operations_acl.go` | EIP-2929 functions use `evm.GasSchedule.GetOr()` |
| `execution/vm/jump_table.go` | Added `SetConstantGas()`, `GetConstantGas()` methods |
| `execution/vm/interpreter.go` | Added `GetBaseJumpTable()`, `CustomJumpTable` support |

### Gas Override Keys

These keys are used in `GasSchedule.Overrides` map:

| Key | Used By | Description |
|-----|---------|-------------|
| `SLOAD_COLD` | gasSLoadEIP2929, makeGasSStoreFunc | Cold storage read (2100) |
| `SLOAD_WARM` | gasSLoadEIP2929, makeGasSStoreFunc | Warm storage read (100) |
| `SSTORE_SET` | makeGasSStoreFunc | Storage slot creation (20000) |
| `SSTORE_RESET` | makeGasSStoreFunc | Storage slot modification (2900) |
| `CALL_COLD` | makeCallVariantGas*, gasEip2929AccountCheck, etc. | Cold account access (2600) |
| `CALL_WARM` | makeCallVariantGas*, gasEip2929AccountCheck, etc. | Warm account access (100) |
| `CALL_VALUE_XFER` | statelessGasCall, statelessGasCallCode | Value transfer gas (9000) |
| `CALL_NEW_ACCOUNT` | statefulGasCall | New account creation (25000) |
| `KECCAK256_WORD` | gasKeccak256, gasCreate2, gasCreate2Eip3860 | Per-word hash cost (6) |
| `COPY` | memoryCopierGas | Per-word copy cost (3) |
| `LOG` | makeGasLog | Base log cost (375) |
| `LOG_TOPIC` | makeGasLog | Per-topic cost (375) |
| `LOG_DATA` | makeGasLog | Per-byte data cost (8) |
| `EXP_BYTE` | gasExpFrontier, gasExpEIP160 | Per-byte exponent cost (10/50) |
| `CREATE_BY_SELFDESTRUCT` | makeSelfdestructGasFn, gasSelfdestruct | Selfdestruct to new account (25000) |
| `INIT_CODE_WORD` | gasCreateEip3860, gasCreate2Eip3860 | Per-word init code cost (2) |
| `MEMORY` | (not patched) | Memory expansion gas (3) |

Constant gas opcodes (ADD, MUL, PUSH*, etc.) are overridden via `CustomJumpTable.SetConstantGas()`.

### Execution Flow

```
SimulateBlockGas(req)
  └─ for each tx: executeTransactionDual()
       ├─ executeSingleTransaction(gasSchedule=nil)     # Original
       │    └─ protocol.ApplyMessage() with standard costs
       └─ executeSingleTransaction(gasSchedule=custom)  # Simulated
            ├─ BuildCustomJumpTable() → constant gas overrides
            ├─ evm.GasSchedule = schedule.ToVMGasSchedule()
            └─ protocol.ApplyMessage() with custom costs
                 └─ Gas functions call evm.GasSchedule.GetOr(key, default)
```

### Not Patched

- `memoryGasCost` - Memory expansion uses quadratic formula outside EVM context
- Legacy pre-Berlin SSTORE functions (gasSStore, gasSStoreEIP2200) - rarely used on mainnet
- Refund calculations - only gas charges are overridable

### Maintenance Note

`GasScheduleForRules()` in `custom_gas.go` returns defaults for the API:
- **Constant gas opcodes**: Auto-updated from JumpTable (no maintenance needed)
- **Dynamic gas defaults**: Hardcoded - if a future EIP changes them, update this function (like EXP_BYTE for Spurious Dragon)

### What We Delegate to Erigon

- **Intrinsic gas**: Uses `fixedgas.IntrinsicGas()` - handles all EIP-specific logic
- **Opcode names**: Uses `vm.OpCode.String()` - opcodeMap built dynamically
- **Constant gas costs**: Uses `vm.GetBaseJumpTable()` - fork-aware JumpTable

## RPC Endpoints

| Method | Description |
|--------|-------------|
| `xatu_getGasSchedule` | Get default gas values for a block's fork |
| `xatu_simulateBlockGas` | Re-execute block with custom gas schedule |
| `xatu_simulateTransactionGas` | Re-execute single transaction with custom gas |

## Testing

```bash
# Get gas schedule for a block
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"xatu_getGasSchedule","params":[21000000],"id":1}'

# Simulate block with custom SLOAD cost
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"xatu_simulateBlockGas","params":[{
    "blockNumber": 21000000,
    "gasSchedule": {"overrides": {"SLOAD_COLD": 1500}}
  }],"id":1}'
```

## Build Tags

- `embedded` - Enables Xatu/simulation features (required)
- `nosqlite`, `noboltdb`, `nosilkworm` - Disable unused backends

## Related Projects

- **lab** - Frontend that consumes simulation API (`/ethereum/execution/gas-profiler/simulate`)
- **erigon** - Upstream Erigon (github.com/erigontech/erigon)
