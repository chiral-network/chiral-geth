# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

chiral-geth is a fork of [core-geth](https://github.com/etclabscore/core-geth) (etclabscore's downstream of ethereum/go-ethereum, v1.12.x line). The project goal is a new PoW blockchain ("Chiral") built on this codebase, replacing ethash with **RandomX v2** (https://github.com/SChernykh/RandomX/tree/v2) as the proof-of-work algorithm. See `PLAN.md` for the migration plan.

- Development happens on the `chiral` branch; `master` tracks upstream core-geth.
- The Go module path is still `github.com/ethereum/go-ethereum` — all imports use the upstream path even though this is a fork.
- Requires Go 1.21+ and cgo (`consensus/lyra2` already compiles C via cgo).

## Commands

- Build geth: `make geth` → `./build/bin/geth` (equivalent: `go run build/ci.go install ./cmd/geth`)
- Build all executables: `make all`
- Full test suite: `make test` (slow, 20m timeout, runs via `build/ci.go`)
- Single package: `go test ./consensus/ethash/`
- Single test: `go test ./consensus/ethash/ -run TestCalcDifficulty`
- Lint: `make lint` (golangci-lint driven by `build/ci.go`)
- core-geth-specific chain-config/consensus equivalence suites: `make test-coregeth`
- Serve docs (mkdocs): `make mkdocs-serve`
- Install code generators (stringer, gencodec, ...): `make devtools`; regenerate generated files (e.g. `eth/ethconfig/gen_config.go`) with `go generate` after changing their sources.

## Architecture

Standard go-ethereum layout (`cmd/geth` CLI entrypoint, `core` chain/state processing, `eth` protocol backend, `miner`, `p2p`, `trie`, ...). The two areas that differ most from upstream go-ethereum, and the ones this project revolves around:

### Chain configuration (params/)

- Chain configs are **interfaces, not structs**: `ctypes.ChainConfigurator` (`params/types/ctypes/configurator_iface.go`). Two implementations: `coregeth.CoreGethChainConfig` (`params/types/coregeth/`, activation-map style used by ETC/Chiral) and upstream-style `goethereum.ChainConfig` (`params/types/goethereum/`). All consensus/core code must use getters (`GetChainID()`, `IsEnabled(c.GetEIP1559Transition, num)`, ...) rather than struct fields.
- `params/confp/` converts and compares config implementations; `params/types/genesisT/` holds genesis types.
- A chain is defined by a file set in `params/`: `config_<chain>.go`, `genesis_<chain>.go`, `alloc_<chain>.go`, `bootnodes_<chain>.go` (see `mintme` for the smallest example, plus `classic`, `mordor`), a CLI flag in `cmd/utils/flags.go`, and wiring in `cmd/geth/main.go` and `core/genesis.go`.
- `params/vars/` holds protocol constants; `params/mutations/` holds reward/fork state-mutation logic (e.g. ECIP-1017 era rewards).

### Consensus engines (consensus/)

- `consensus.Engine` interface: `consensus/consensus.go`. Implementations: `ethash` (incl. ETC's ECIP-1099 "etchash" DAG changes), `clique` (PoA), `lyra2` (cgo-based PoW used by MintMe), `beacon` (post-merge wrapper — `CreateConsensusEngine` wraps *every* engine in `beacon.New(...)`; harmless for pure-PoW chains that never reach a TTD).
- Engine selection flows from the genesis config: `Config.GetConsensusEngineType()` → `ctypes.ConsensusEngineT_*` enum (`params/types/ctypes/types.go`) → `eth/backend.go` builds the engine-specific config → `ethconfig.CreateConsensusEngine` (`eth/ethconfig/config.go`).
- `consensus/lyra2` is the smallest complete template for adding a new PoW engine: cgo hash binding (`lyra2.go`), header rules/difficulty/rewards (`consensus.go`), CPU + remote sealer (`sealer.go`), `eth_getWork`-style RPC (`api.go`). The full integration footprint of an engine+chain is discoverable with `grep -rl "lyra2\|mintme" --include="*.go" .`
- Caveat when modeling on lyra2: its `SealHash` omits `BaseFee`; use `ethash.SealHash` (`consensus/ethash/consensus.go`) as the reference — it appends `BaseFee`/withdrawal fields conditionally, which matters once EIP-1559 is enabled.

### Sync note for heavy PoW

Full sync verifies every header's seal; snap sync verifies every 100th (`fsHeaderCheckFrequency` in `eth/downloader/downloader.go`). This matters when the PoW hash is expensive to verify (RandomX light mode is ~ms per hash).

## Conventions

- Commit messages are prefixed with the package(s) touched, e.g. `consensus/randomx: add seed epoch schedule` (core-geth/go-ethereum convention).
- Code must be gofmt-clean; `make lint` must pass.
