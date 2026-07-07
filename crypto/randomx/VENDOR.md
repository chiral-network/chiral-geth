# Vendored RandomX

`librandomx/` is a vendored copy of RandomX with the v2 algorithm revision:

- Upstream: https://github.com/SChernykh/RandomX branch `v2`
  (fork of https://github.com/tevador/RandomX carrying the RandomX v2 changes)
- Pinned commit: `2b9ab3e9d380fc2ec86a103a5034ea13c0ce62f4` (2026-02-14, "Fixed misaligned access")
- Copied: `LICENSE`, `CMakeLists.txt` (reference only, not used by the build),
  `src/` (minus `src/tests/`), `doc/` (design/specs/configuration references).
- Local modifications:
  1. `src/configuration.h`: `RANDOMX_ARGON_SALT` changed from the stock
     `"RandomX\x03"` to **`"RandomX-Chiral\x01"`** (decided 2026-07-06; see
     docs/chiral/launch-parameters.md). Every Chiral hash differs from
     stock-configured RandomX, so Monero-ecosystem hashpower cannot be
     redirected at the network unmodified. Miner-facing algorithm id:
     `rx/chiral`. All other parameters remain stock. The vendored build
     reproduced the official stock v2 vectors bit-exactly *before* this
     change (commit 888d0ae72); the Chiral vectors in `randomx_test.go`
     were derived with that validated build.

  Cache-buster: Go's build cache does not see edits under `librandomx/`
  (only files in the package directory are hashed), so any change to the
  vendored sources must be accompanied by bumping `CHIRAL_RANDOMX_CONFIG`
  in the `#cgo` flags in `randomx.go` — otherwise stale objects are reused
  silently.

## How it builds

cgo compiles the `rx_*.c`, `rx_*.cpp` and `rx_*.S` forwarding files in this
directory; each one `#include`s exactly one upstream source file. The set of
forwarding files mirrors the `randomx_sources` list in the upstream
`CMakeLists.txt` plus the per-arch JIT sources (`_amd64`/`_arm64` filename
suffixes reproduce CMake's arch conditionals via Go's build constraints).
Compiler flags in `randomx.go` mirror the upstream defaults: `-maes` on x86-64,
`-march=armv8-a+crypto` (+`HAVE_HWCAP` on Linux) for ARM64.

Intentional deviations from the CMake build:

- `argon2_ssse3.c` / `argon2_avx2.c` get per-file `-mssse3`/`-mavx2` flags in
  CMake; cgo has no per-file flags, so they are compiled without them. Their
  bodies are `#if defined(__SSSE3__)/__AVX2__` guarded, so they compile to the
  null-implementation stubs and RandomX falls back to the reference Argon2 at
  runtime. Cost: slower cache initialization (~1 s once per seed epoch); hash
  correctness and speed are unaffected.
- RISC-V sources are not wired up; supported GOARCHes are amd64 and arm64
  (other architectures will fail to link the JIT symbols).

## How to update

1. `git clone -b v2 https://github.com/SChernykh/RandomX /tmp/RandomX && git -C /tmp/RandomX rev-parse HEAD`
2. Replace `librandomx/` contents as listed above (keep `src/tests/` excluded).
3. Diff upstream `CMakeLists.txt` `randomx_sources` against the `rx_*` files;
   add/remove forwarding files to match.
4. Update the v2 test vectors in `randomx_test.go` from
   `/tmp/RandomX/src/tests/tests.cpp` (the `RANDOMX_FLAG_V2` branch of each
   `equalsHex` assert) if upstream changed them.
5. Update the pinned commit above; run `go test ./crypto/randomx/` (without
   `-short`, needs ~2.4 GiB).
