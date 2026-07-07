# Chiral launch parameters

Working notes on the network parameters affected by replacing ethash with
RandomX v2, what has been decided, what remains open, and the two precedents
this fork's lineage offers (MintMe's 2021 relaunch and Webchain's 2018
launch). Companion to `PLAN.md` at the repo root.

Status of each parameter is one of: **set** (implemented on `chiral`),
**decided** (confirmed by the project owner), or **open** (needs a decision
before genesis). As of 2026-07-06 **no parameter remains open**: the only
value not yet in the tree is the genesis timestamp, fixed by procedure at
the launch ceremony (see "Genesis ceremony" below).

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
| RandomX salt | **`RANDOMX_ARGON_SALT = "RandomX-Chiral\x01"`** — **decided 2026-07-06** | Unique per-chain configuration (upstream-recommended): stock Monero-ecosystem hashpower (pools, rentals, botnets) computes wrong hashes for Chiral, eliminating zero-effort hashpower redirection — the dominant launch-day 51% vector given Monero's ~5 GH/s vs. our kH/s scale. Not cryptographic protection (a one-line patch defeats it), but it removes the accident/rental/botnet mass. Cost: stock XMRig needs a patched build; the miner-facing algo id is `rx/chiral` and upstreaming an XMRig variant (à la Wownero's `rx/wow`) is a pre-launch task. All other RandomX parameters stay stock. Chiral test vectors re-derived from a build first validated against official stock vectors (`crypto/randomx/VENDOR.md`). |
| Seal semantics | `MixDigest` = seed-hash commitment; PoW = `RandomX_v2(key, sealHash ‖ nonce) ≤ 2^256/difficulty` | `eth_getWork`'s second slot now returns the RandomX key (seed hash) — external-miner documentation must say so. |
| Seed epochs | **16384 blocks, 256 lag** — **decided 2026-07-06** (set in `ChiralChainConfig`; raw engine defaults remain Monero's 2048/64) | Monero's numbers were tuned for 2-minute blocks; at 13 s they'd rotate the key every ~7.4 h with ~14 min notice, forcing frequent 2.1 GiB dataset rebuilds (30–60 s on small CPUs). 16384/256 restores Monero's wall-clock cadence: rotation every ~2.5 days with ~55 min of rebuild notice. |
| Chain identity | **chainID = networkID = 618033** — **decided 2026-07-06** | Golden-ratio digits (1.618033…). First choices 61803/161803 are already registered on chainid.network (checked 2026-07-06); 618033 is free — submitting it to `ethereum-lists/chains` is a launch task. Kept equal on purpose; never to change (Webchain churned identifiers twice, pure downstream pain). |
| Monetary policy | **50 CHI × (249/250)^era, era = 100,000 blocks; uncle & nephew = subsidy/32; no premine** — **decided 2026-07-06** | One geometric curve, chosen once, never to be edited (the Webchain lesson). Base-emission cap = 50 × 100,000 × 250 = **1.25B CHI**, half emitted in ~173 eras ≈ 7.1 years at 13 s blocks (uncles add a few percent on top). Empty genesis allocation: fair launch. Precedented shape (Webchain/MintMe run the same ratio at 100k eras since 2021). |
| Gas limit / block time | **8,000,000 gas / ~13 s** — **decided 2026-07-06** | Conservative for a young CPU-verified chain; EIP-1559 elasticity allows 16M bursts, and miners can vote the target up later without a fork. 13 s keeps the EIP-100 `//9` machinery on well-trodden ground; epoch-boundary cache builds (~1 s) argue against aggressive cuts. |
| Verifier memory bounds | ≤3 epoch caches (~768 MiB) verifying; ≤2 datasets (~4.2 GiB) mining | New parameter class vs. ethash; a verify-only node's RAM floor is now ~0.5–1 GiB. |
| Uncle rules, future-block tolerance | max 2 uncles / depth 7; 15 s | Propagation-driven, not hash-driven — deliberately unchanged. |
| Nonce width | 8 bytes (unchanged) | Astronomically sufficient at CPU-network hashrates. |

## Launch-task parameters (decided 2026-07-06)

The remaining work items are operational, but each had parameters of its
own. Those are now chosen:

### Genesis ceremony

- **extraData: `"chiral: non-superimposable"`** (26 bytes; header limit 32)
  — **set**, hash re-pinned. The definition of chirality — an object
  distinct from its own mirror image — as the chain's permanent genesis
  motto. Chosen now rather than at ceremony so the hash churns once, not
  twice.
- **Timestamp: set at ceremony, not before.** Policy: announce a launch
  minute publicly in advance; at ceremony set `Timestamp` to exactly that
  unix minute, re-pin `ChiralGenesisHash` (`TestGenesisHashes` prints it),
  tag the release, and publish binaries built from the tag. A timestamp
  chosen today would either create a hard cannot-mine-before date (genesis
  in the future violates the future-block rule for block 1) or a stale
  past date; neither is worth the fake precision. This is deliberately the
  **only** field left open — everything else in the genesis block is final.
- Allocation stays empty (fair launch, decided above); nonce 0.

### Bootnodes

- **Three bootnodes at launch, across at least two providers and two
  regions**, listed as static enodes in `params/bootnodes_chiral.go`.
  Three is the smallest count where one failure plus one maintenance still
  leaves a discovery entry point; provider diversity avoids a single-AS
  eclipse of the discovery layer. ETC ships a similar handful.
- Standard port **30303** (no reason to diverge from the ecosystem
  default; NAT'd home miners benefit from unsurprising firewall rules).
- Node keys generated on the hosts (`geth --chiral` generates
  `nodekey` on first start), never reused across hosts, backed up offline
  — a bootnode's identity is its enode, so key loss silently invalidates
  the shipped list.
- Bootnodes run **non-mining** with default peer limits; DNS discovery
  (EIP-1459 trees) is a post-launch nicety, not a launch requirement —
  static enodes bootstrap fine at this scale.

### Testnet: **Levo**

Named for levorotation — the left-handed enantiomer to the mainnet's
right. Parameters:

- **chainID = networkID = 1618033** (the full golden-ratio digits
  1.618033…; unregistered on chainid.network as of 2026-07-06 — register
  together with mainnet's 618033).
- **Seed epochs 2048/64** — deliberately *not* mainnet's 16384/256: at
  13 s blocks the testnet rotates keys ~3× a day, so epoch transitions,
  dataset rebuilds and boundary reorgs — the machinery mainnet most fears
  — get exercised constantly instead of every 2.5 days.
- Everything else mirrors mainnet exactly (same salt — one `rx/chiral`
  toolchain for both networks, as Mordor shares etchash with ETC; same
  reward curve, same difficulty constants, same fork set), because a
  testnet that diverges from mainnet consensus parameters tests the wrong
  chain.
- Implementation is the mintme-pattern file set (`config_levo.go`,
  `genesis_levo.go`, `bootnodes_levo.go`, `--levo` flag) — tracked as the
  next code task.

### Miner ecosystem

- Ship a patched XMRig as **`chiral-network/xmrig`** (fork, not rewrite),
  adding algorithm id **`rx/chiral`** = stock RandomX v2 with
  `ARGON_SALT "RandomX-Chiral\x01"`; submit the variant upstream once the
  network is live (Wownero's `rx/wow` is the accepted precedent for
  salt-only variants). Until upstream merges, the fork is the supported
  miner; `geth --chiral --mine --miner.threads=N` remains the zero-setup
  fallback.
- Pool operators verify shares with the same vendored library
  (`crypto/randomx`) or any RandomX build with the one-line salt patch.

### Registry & CI

- **Registry**: one PR to `ethereum-lists/chains` registering 618033
  (Chiral) and 1618033 (Levo) together, after the genesis ceremony fixes
  the final hashes (the registry entry embeds no hash, but announcing
  once, with final values, avoids churn).
- **CI toolchain**: bump golangci-lint to the latest stable release
  (anything ≥ Go 1.24 support), in a dedicated commit that also absorbs
  the finding churn across upstream code — kept separate from feature
  work so the diff stays reviewable. The repo's current pinned linter
  predates Go 1.24 and emits bogus `typecheck` errors repo-wide.

### MESS (ECBP-1100) — **disabled by decision (2026-07-07)**

Not part of Chiral consensus policy. Rationale:

- MESS is subjective fork scoring: nodes weigh reorgs by local arrival
  time, so under a network partition two segments can finalize different
  branches — a consensus-split vector, not just a tuning knob.
- Its own safety gating (minimum peer count, stale-head auto-off) makes it
  ineffective exactly when a young chain is most fragile, and forcing it
  on (`--ecbp1100.nodisable`) in a sparse mesh maximizes the split risk.
- The precedent chain arc ended in removal: ETC activated it as a
  post-attack stopgap (block 11.38M, Oct 2020) and deactivated it at
  Spiral (block 19.25M, Jan 2023). Chiral's launch-day 51% defenses are
  the unique `rx/chiral` salt and deep-confirmation guidance instead.

Mechanics of the disablement: `ECBP1100FBlock` is explicitly nil in
`ChiralChainConfig` (the machinery stays in the tree — it is inherited
core-geth code and other configs use it), and a node started with the
generic `--ecbp1100` override on a RandomX network logs a prominent
warning that local activation risks diverging from the network. Any
future adoption would be a fresh decision plus a coordinated fork — not a
scheduled revisit.

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
