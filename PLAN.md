# Chiral: RandomX v2 proof-of-work migration plan

> **Status (2026-07-06)**: Phases 1–4 complete, Phase 5 mostly complete on the
> `chiral` branch. `geth --chiral --mine --miner.threads=N` mines RandomX v2
> blocks and peers sync/verify them. Defaults adopted pending confirmation:
> chainID/networkID 61803, 2048/64 seed epochs, stock RandomX config
> (unique-salt decision open), 2 CHI placeholder reward. Genesis difficulty
> = MinimumDifficulty (bootstrap-from-minimum, decided 2026-07-06; see
> docs/chiral/launch-parameters.md). Note: `--mine` alone only serves remote miners (upstream
> semantics); local hashing needs `--miner.threads=N`. Known env caveat: the
> repo's pinned golangci-lint predates Go 1.24 and emits bogus typecheck
> errors repo-wide (pre-existing; CI toolchain bump tracked for Phase 6).

Goal: turn this core-geth fork into a new PoW blockchain ("Chiral") whose consensus hash is
**RandomX v2** (https://github.com/SChernykh/RandomX/tree/v2, `doc/design_v2.md`), replacing
ethash. Everything else (EVM, state, p2p, RPC) stays geth-compatible.

## Why this is tractable here

core-geth already supports multiple consensus engines selected per-chain via
`ChainConfigurator.GetConsensusEngineType()`, and already ships a cgo-based PoW engine
(`consensus/lyra2`, used by the MintMe chain). Adding RandomX is "one more engine + one more
chain definition", not a structural rewrite. The lyra2/mintme footprint
(`grep -rl "lyra2\|mintme" --include="*.go" .`) is the integration checklist.

The one genuinely new problem RandomX brings (vs. lyra2's stateless hash) is **keyed state**:
hashes are computed against a ~2 GiB Dataset (fast/mining mode) or ~256 MiB Cache (light/verify
mode) that is derived from a **key** which must rotate on an epoch schedule. The engine therefore
needs chain access to derive keys, and cache/dataset lifecycle management (like ethash's DAG
epochs, which gives us the pattern: `consensus/ethash/ethash.go` LRU of per-epoch caches).

## RandomX v2 facts that drive the design

From `doc/design_v2.md` + `doc/configuration.md` (fetched 2026-07-06):

- v2 is a parameter/VM tweak of v1 (CFROUND every 16th execution, AES register mixing,
  program size 256→384, 2-iteration prefetch). The **external API and integration surface are
  unchanged** from v1: same cache/dataset/VM/key model, 32-byte hash output.
- Memory: Cache 256 MiB (`RANDOMX_ARGON_MEMORY` = 262144 KiB), Dataset ~2080 MiB
  (`DATASET_BASE_SIZE` 2 GiB + `EXTRA_SIZE` 32 MiB). Light mode ≈ 10–20× slower than fast mode
  (`RANDOMX_CACHE_ACCESSES` = 8).
- The project explicitly recommends each chain pick a **unique configuration** (at minimum a
  unique `RANDOMX_ARGON_SALT`) so rental/botnet hashpower tuned for Monero can't be pointed at
  the new chain unmodified. Safe-to-change knobs are listed in `configuration.md`; compile-time
  checks catch unsafe combos.
- v2 is pre-release (targeted at a future Monero fork). We must **pin an exact commit** and
  vendor it; track upstream until the parameters freeze.

## Design decisions (proposed defaults — confirm before Phase 2)

1. **Chain identity**: name Chiral, new `ChainID`/`NetworkID` (pick unused values, e.g. from
   chainlist.org; placeholder: 61803). Files: `params/config_chiral.go`, `genesis_chiral.go`,
   `alloc_chiral.go`, `bootnodes_chiral.go`, `--chiral` flag. Genesis enables all ETH forks
   through Shanghai at block 0 (modern EVM from day one), PoW forever (no TTD).
2. **RandomX configuration**: adopt v2 defaults but set `RANDOMX_ARGON_SALT = "Chiral\x01"`
   (unique per configuration.md guidance). Cost: stock XMRig can't mine Chiral without a patch;
   benefit: immunity to zero-effort hashpower redirection. (If instant miner-ecosystem
   compatibility matters more, keep stock parameters — decision point.)
3. **Key (seed) epoch schedule**: Monero-style — `SeedEpochLength = 2048` blocks,
   `SeedLag = 64`. Key for height *h* = header hash of block
   `floor((h - SeedLag) / SeedEpochLength) * SeedEpochLength` (genesis hash for the first
   epochs). The lag gives miners/verifiers 64 blocks (~15 min) to precompute the next dataset.
4. **Header semantics**: keep `types.Header` unchanged (zero storage-format divergence).
   - PoW input: `SealHash(header)` (ethash-style RLP hash that includes `BaseFee` etc.,
     *not* lyra2's which omits it) concatenated with the 8-byte `Nonce`.
   - PoW check: `RandomXHash(key(h), sealHash || nonce)` interpreted big-endian must be
     ≤ `2^256 / Difficulty`.
   - `MixDigest` must equal the epoch **seed hash** — cheap commitment that catches
     wrong-epoch mining early and gives external miners an explicit key field (ethash's
     getWork already has a seedHash slot, so `eth_getWork`/`submitWork` shapes still fit).
5. **Difficulty**: reuse the EIP-100-family algorithm (lyra2's `CalcDifficulty` is a clean
   standalone copy) with target block time ~13 s and a suitable `MinimumDifficulty` for CPU
   hashrates (RandomX ≈ 10⁴ H/s per high-end CPU, ~9 orders of magnitude below ethash GPUs —
   ethash's minimum-difficulty constants are far too high).
6. **Uncles & rewards**: keep geth's uncle machinery (maxUncles=2, reward/32 style) — removing
   GHOST touches far more code than keeping it. Monetary policy is an open product decision;
   placeholder: constant 2 CHI/block with ECIP-1017-style disinflation as an option later
   (`params/mutations/` is where reward policy lives; lyra2's `accumulateRewards` is the
   self-contained template).
7. **Library linkage**: vendor SChernykh/RandomX@v2 (pinned commit) into
   `crypto/randomx/librandomx/` and build it from cgo directly (RandomX is plain C++11 +
   assembly; cgo compiles `.cpp`/`.S` files in-package — same approach lyra2 uses for C).
   Fallback if cgo-direct proves brittle on some platform: a `build/randomx.sh` that builds
   `librandomx.a` via CMake into `build/_workspace` (the hera/evmone precedent) and
   `#cgo LDFLAGS` against it. JIT on x86-64 + ARM64, interpreter fallback elsewhere;
   `RANDOMX_FLAG_SECURE` (W^X) on by default.

## Phases

### Phase 1 — Go binding: `crypto/randomx`
- Vendor pinned RandomX v2 sources; apply Chiral `configuration.h` (salt change).
- cgo wrapper exposing: `Flags` detection (JIT/AES/large-pages/secure), `NewCache(key)`,
  `NewDataset(cache, threads)`, `NewVM(cache|dataset)`, `(vm).Hash(input)`,
  `(vm).HashFirst/HashNext` (batched mining), all with finalizers + explicit `Close`.
- Thread-safety: VM per goroutine; cache/dataset shared read-only after init.
- Tests: upstream v2 test vectors (from `src/tests/tests.cpp` on the pinned commit) in both
  light and fast mode, plus a light≡fast equivalence test. Gate the 2 GiB fast-mode test behind
  `-short`/env so CI stays viable.
- CI: ensure `build/ci.go` test/lint pass with the new cgo package on linux/amd64 first;
  macOS/arm64 next; Windows last (or explicitly deferred).

### Phase 2 — Consensus engine: `consensus/randomx`
Modeled file-by-file on `consensus/lyra2` + the stateful parts of `consensus/ethash`:
- `randomx.go` — engine struct; config (mode: mine/verify, dataset dir opt-out, thread counts);
  **epoch manager**: `seedHash(chain, height)` via ChainHeaderReader; LRU of Caches (verify)
  and at most one Dataset + prefetched successor (mining), mirroring ethash's cache/dataset LRU.
- `consensus.go` — Author/VerifyHeader(s)/VerifyUncles/Prepare/Finalize(+Assemble)/SealHash/
  CalcDifficulty; `verifySeal` computes the light-mode hash and checks target + MixDigest==seedHash.
  Copy ethash's SealHash (BaseFee/withdrawals aware), lyra2's header-rule structure.
- `sealer.go` — CPU miner over the fast-mode dataset (per-thread VM, `HashFirst/HashNext`
  batch loop) + remote sealer (`eth_getWork` returns sealHash/seedHash/target/blockNumber —
  same 4-tuple as ethash).
- `api.go` — getWork/submitWork/hashrate RPC, from lyra2's.
- `difficulty.go` — EIP-100 variant + Chiral constants.
- Engine unit tests: seal→verify roundtrip (tiny-difficulty test config), epoch-boundary
  verification, reorg across an epoch boundary, wrong-MixDigest rejection, remote-sealer flow
  (mirror `lyra2/sealer_test.go`, `ethash/sealer_test.go`).

### Phase 3 — Config plumbing: params/
- `ctypes`: add `ConsensusEngineT_RandomX` + `RandomXConfig{}` (+ `IsRandomX()`), following the
  Lyra2 additions in `params/types/ctypes/types.go`.
- Add the engine field + `GetConsensusEngineType` handling to both configurator
  implementations (`params/types/coregeth`, `params/types/goethereum`) and `params/confp`
  conversion; `genesisT` passthrough.
- Chain definition: `params/config_chiral.go` (CoreGethChainConfig, all-forks-at-0, RandomX
  engine), `genesis_chiral.go`, `alloc_chiral.go` (premine/dev-fund decision), 
  `bootnodes_chiral.go` (empty until Phase 6), `params/vars/chiral.go` (block time, min
  difficulty, seed epoch constants).
- Run `make test-coregeth-features-coregeth` equivalence suite to keep other chains intact.

### Phase 4 — Node wiring
- `eth/backend.go`: build `*randomx.Config` when `GetConsensusEngineType() == ConsensusEngineT_RandomX`.
- `eth/ethconfig/config.go`: extend `CreateConsensusEngine(...)` signature + fake/test modes
  (`NewFaker`/`NewTester` equivalents so core tests can run without real hashing);
  regenerate `gen_config.go`.
- `cmd/utils/flags.go` + `cmd/geth/main.go`: `--chiral` network flag (mintme pattern),
  `--randomx.*` flags (mode light/fast for non-mining nodes, dataset init threads, large pages);
  `cmd/echainspec` chain registration.
- `core/genesis.go`: genesis default hookup.
- Smoke test: single-node `--chiral --mine` produces blocks; two-node private net syncs both
  full and snap (note: snap sync seal-checks every 100th header — fine; full sync of a long
  chain in light mode is the slow path, acceptable while the chain is young).

### Phase 5 — Hardening & tests
- Difficulty tests (tests-generate-difficulty machinery) for the Chiral algorithm.
- Long-run miner soak (epoch rotation twice: >4224 blocks at test epoch length — shrink
  `SeedEpochLength` in test config to keep this fast).
- Fuzz/negative: corrupt nonce, stale-epoch seal, future-epoch seal, difficulty=0.
- Memory-pressure review: verify-only node ≈ 256 MiB × cached epochs (bound LRU at 2–3);
  mining node ≈ 2.1 GiB (+ optional 2.1 GiB during next-epoch prefetch) — document minimums.
- `make lint`, full `make test`, plus existing regression suites green.

### Phase 6 — Network launch
- Finalize genesis (chainID, alloc, extraData ceremony), regenerate genesis hash constants.
- Bootnode deployment + `bootnodes_chiral.go`; optional DNS discovery later.
- Testnet first (chiral-testnet config, mordor-analog), then mainnet.
- Ops/docs: README network table row, docs/ page, Dockerfile check (cgo/C++ toolchain in the
  builder image), release builds for linux/amd64+arm64 first.
- Miner ecosystem: built-in CPU miner + getWork suffices at launch; XMRig patch / stratum
  proxy as a fast-follow.

## Risks / open questions

- **RandomX v2 is pre-release**: parameters may still change upstream → pin commit, isolate
  everything version-specific in `crypto/randomx`, re-run test vectors on bumps.
- **cgo/C++ build portability** (esp. Windows, and JIT `.S`/`.asm` files under cgo): Phase 1
  proves this early; CMake-static-lib fallback documented above.
- **Light-mode verify cost** (~ms/hash): full sync speed degrades as the chain grows; snap
  sync (1/100 seals) is the mitigation story, and a `--randomx.mode=fast` verify option can
  trade 2 GiB RAM for ~15× verify speed.
- **Fake/test engine coverage**: much of geth's test suite assumes cheap PoW — faker modes in
  Phase 4 are load-bearing, not optional.
- **Product decisions needed from you**: chain ID, monetary policy/premine, block time, and
  the unique-RandomX-config vs. stock-miner-compatibility tradeoff (decision 2).

## Suggested execution order

Phase 1 alone proves the riskiest unknown (building RandomX v2 under cgo) and is independently
testable; Phases 2–4 land as one reviewable arc on `chiral` (engine → params → wiring, each
compiling green); 5–6 follow. First concrete step: vendor the pinned v2 sources and get
`crypto/randomx` hashing the upstream test vectors in light mode on linux/amd64.
