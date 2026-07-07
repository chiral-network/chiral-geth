# Chiral launch parameters

Working notes on the network parameters affected by replacing ethash with
RandomX v2, what has been decided, what remains open, and the two precedents
this fork's lineage offers (MintMe's 2021 relaunch and Webchain's 2018
launch). Companion to `PLAN.md` at the repo root.

Status of each parameter is one of: **set** (implemented on `chiral`),
**decided** (confirmed by the project owner), or **open** (needs a decision
before genesis).

## Why the hash change moves parameters at all

Two properties of RandomX v2 differ from ethash by orders of magnitude:

1. **Network hashrate scale.** CPUs hash at ~1–10 kH/s each (vs. GPU/ASIC
   farms at MH/s–TH/s per device). Every difficulty-denominated constant
   inherited from ethash mainnet is ~8–9 orders of magnitude too large.
2. **Stateful, expensive verification.** Verifying one seal costs ~1 ms (JIT
   light mode) against a per-epoch 256 MiB cache that takes ~1 s to build;
   mining needs a ~2.1 GiB per-epoch dataset. Ethash verification was ~50 µs
   against a 16–64 MiB cache. This raises node memory floors and makes seed
   epoch cadence a real parameter.

## Parameters set on the `chiral` branch

| Parameter | Value | Rationale |
|---|---|---|
| `MinimumDifficulty` | 10,000 | Floor ≈ 770 H/s sustaining 13 s blocks — keeps the chain alive even if hashrate collapses to a single machine. Production-validated on MintMe (lyra2, same value) since 2021. |
| `DifficultyBoundDivisor` | 200 | Up to ±10%/block adjustment (ethash: 2048, ±5% max). CPU-network hashrate is volatile — a pool or botnet joining can 10× it overnight — so convergence speed beats smoothness. Also MintMe-validated. |
| Difficulty algorithm | EIP-100 family, `//9` term (~13 s target), no bomb (`DisposalBlock = 0`) | The bomb was an ethash-era policy device; nothing about RandomX wants one. |
| Genesis difficulty | **10,000 (= `MinimumDifficulty`)** — **decided 2026-07-06** | Bootstrap-from-minimum: no launch-hashrate prediction needed; the retarget climbs +10%/block, reaching any plausible launch hashrate within a few hours. Precedent: Webchain launched at difficulty `0x1` with the same divisor and was fine. The instamine window is minutes long and bounded by the floor. |
| Seal semantics | `MixDigest` = seed-hash commitment; PoW = `RandomX_v2(key, sealHash ‖ nonce) ≤ 2^256/difficulty` | `eth_getWork`'s second slot now returns the RandomX key (seed hash) — external-miner documentation must say so. |
| Seed epochs | 2048 blocks, 64 lag (engine defaults; chain-config overridable) | Monero's literal numbers; see the open item below — these were tuned for 2-minute blocks. |
| Verifier memory bounds | ≤3 epoch caches (~768 MiB) verifying; ≤2 datasets (~4.2 GiB) mining | New parameter class vs. ethash; a verify-only node's RAM floor is now ~0.5–1 GiB. |
| Uncle rules, future-block tolerance | max 2 uncles / depth 7; 15 s | Propagation-driven, not hash-driven — deliberately unchanged. |
| Nonce width | 8 bytes (unchanged) | Astronomically sufficient at CPU-network hashrates. |

## Open parameters

1. **Seed epoch schedule** *(recommendation: `RandomXSeedEpochLength = 16384`,
   `RandomXSeedEpochLag = 256`)*. At 13 s blocks the Monero-default 2048/64
   means a key rotation every ~7.4 h with only ~14 min of notice; every
   rotation forces miners to rebuild the 2.1 GiB dataset (2.5 s on a big
   server, 30–60 s on a 4-core desktop) and verifiers to build a new cache.
   16384/256 restores Monero's wall-clock cadence (~2.5 days, ~55 min
   notice). Chain-config fields already exist; this is a genesis-file
   decision.
2. **RandomX configuration uniqueness** — stock parameters (XMRig-compatible
   out of the box) vs. a Chiral-unique `RANDOMX_ARGON_SALT` (immune to
   redirected Monero rental/botnet hashpower, but stock miners need a
   patch). Must be decided **before** genesis; it changes every hash. The
   upstream `configuration.md` explicitly recommends per-chain uniqueness.
3. **Chain identity** — chainID/networkID, currently placeholder 61803, kept
   equal on purpose (Webchain/MintMe changed identifiers twice; we should
   pick once and never move).
4. **Monetary policy** — placeholder is a flat 2 CHI/block with
   Frontier-style uncle rewards. Options: keep flat, or a single geometric
   decay in the spirit of Webchain's 50 × (249/250)^era per 100k blocks —
   picked once and never edited (see the Webchain lesson below).
5. **Gas limit / block time** — 8M gas at 13 s inherited as placeholders;
   both are product choices, not hash-algorithm consequences. One RandomX
   caveat: reorgs across an epoch boundary stall verification ~1 s while a
   cache builds, which argues mildly against aggressive block-time cuts.
6. **Genesis ceremony** — timestamp, extraData, any allocation; plus
   bootnodes and a testnet definition.

## Precedent 1: MintMe relaunch (June 2021, in this repo)

The closest production analog: a CPU-PoW (lyra2) chain on this exact
codebase. Its genesis is a *migration* of a running chain, so identity and
allocation numbers are not launch guidance, but the consensus constants are.

- ChainID 24734, NetworkID 37480 (unequal — legacy accident, don't copy)
- Genesis difficulty 0x200000 (2,097,152) — sized for the ~160 kH/s of
  miners the chain already had; not applicable to a fresh launch
- GasLimit 3,141,592 (π); no EIP-1559 ever
- `DifficultyBoundDivisor` 200, `MinimumDifficulty` 10,000, ~13 s blocks —
  the constants Chiral adopted
- Alloc: 18,975 accounts / 3,868 contracts / ~533.77M coins — the final
  state of old Webchain
- Reward at relaunch ≈ 1.17 coins/block, continuing Webchain's decay curve
  via era offsets (+72 block-eras, +865 skipped reward-eras)

## Precedent 2: Webchain original launch (2018, `mintme-com/webchaind`)

From the archived client's `core/config/mainnet.json` and
`core/state_processor.go`:

- **Consensus at genesis: CryptoNight** (Monero's family), network 37129,
  chainID 101 (EIP-155 activated at block 1)
- **Genesis difficulty: `0x1`** — bootstrap-from-zero with divisor 200,
  Homestead-style `1 − Δt//10` retarget (~14 s target)
- GasLimit 0x03000000 (50.3M); genesis nonce 0x43
- **Premine: 350M WEB to one address** (~65% of eventual supply)
- Rewards: 50 WEB/block, 100k-block eras, ×(249/250) per era; uncle and
  nephew rewards = winner/32
- Fork history: chainID → 24484 and **PoW swap #1 CryptoNight → Lyra2**
  (block 2,022,222); **PoW swap #2 → Lyra2v2** (2,619,000); EIP-100
  difficulty + first reward cut (3,300,001); two further ad-hoc reward
  tables with era-skips (3,600,001; 5,000,001); **"Stop" at 7,200,000**,
  state re-genesised into core-geth as MintMe. Integrating the schedule
  gives ~183M mined + 350M premine ≈ the 533.77M migration alloc ✓.

**Lessons drawn:**

1. Bootstrap-from-minimum genesis difficulty works in practice (basis of the
   decided value above).
2. Live PoW swaps mid-chain are proven feasible in this lineage (twice) — a
   fallback if RandomX v2 parameters shift upstream before Monero finalizes
   them, or if a v3 ever becomes necessary.
3. Ad-hoc emission edits become permanent code warts (the +865 era-skip
   still lives in `consensus/lyra2` today). Pick one curve; never touch it.
4. Identifier churn (chainID 101 → 24484 → 24734) is pure downstream pain;
   pick once.
5. A 65%-single-address premine is the cautionary tale on the allocation
   axis.
