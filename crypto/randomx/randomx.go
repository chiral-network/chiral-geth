// Package randomx provides Go bindings for the RandomX proof-of-work virtual
// machine, including the v2 algorithm revision used by the Chiral network.
//
// The RandomX C++ sources are vendored under librandomx/ (see VENDOR.md for
// provenance and update instructions) and compiled directly by cgo through the
// rx_*.c / rx_*.cpp / rx_*.S forwarding files in this directory, mirroring the
// upstream CMake source list.
//
// Usage model (mirrors the C API):
//
//   - Light (verification) mode: allocate a Cache, Init it with the epoch key,
//     create a VM with the cache. ~256 MiB, slower hashing.
//   - Fast (mining) mode: additionally allocate a Dataset, initialize it from
//     the cache (parallelizable), create a VM with FlagFullMem and the dataset.
//     ~2.1 GiB, fast hashing.
//
// Chiral consensus always passes FlagV2; the flag is exposed rather than
// hardcoded so tests can exercise both algorithm revisions against upstream
// test vectors.
//
// Thread-safety: a VM must only be used by one goroutine at a time. Cache and
// Dataset are immutable after initialization and may be shared by any number
// of VMs concurrently. Init/Release calls must not race with users.
package randomx

/*
// CHIRAL_RANDOMX_CONFIG is a cache-buster, not a code switch: Go's build
// cache only tracks files in this directory, so edits under librandomx/
// (e.g. configuration.h) are invisible to it. Bump the number whenever the
// vendored sources change (see VENDOR.md).
#cgo CFLAGS: -O3 -DCHIRAL_RANDOMX_CONFIG=1
#cgo CXXFLAGS: -O3 -std=c++17 -DCHIRAL_RANDOMX_CONFIG=1
#cgo amd64 CFLAGS: -maes
#cgo amd64 CXXFLAGS: -maes
#cgo arm64 CFLAGS: -march=armv8-a+crypto
#cgo arm64 CXXFLAGS: -march=armv8-a+crypto
#cgo linux,arm64 CFLAGS: -DHAVE_HWCAP
#cgo linux,arm64 CXXFLAGS: -DHAVE_HWCAP
#cgo linux LDFLAGS: -lstdc++ -lm
#cgo darwin LDFLAGS: -lc++
#include <stdlib.h>
#include "librandomx/src/randomx.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// HashSize is the size of a RandomX hash in bytes.
const HashSize = C.RANDOMX_HASH_SIZE

// Flags configure RandomX allocation and VM behavior. They mirror
// randomx_flags in randomx.h.
type Flags int

const (
	// FlagDefault selects interpreted mode, software AES and small pages.
	FlagDefault Flags = 0
	// FlagLargePages allocates cache/dataset/scratchpad in huge pages.
	FlagLargePages Flags = 1
	// FlagHardAES uses hardware AES instructions (requires CPU support).
	FlagHardAES Flags = 2
	// FlagFullMem selects fast mode: the VM hashes against a full Dataset.
	FlagFullMem Flags = 4
	// FlagJIT enables the JIT compiler (x86-64/ARM64).
	FlagJIT Flags = 8
	// FlagSecure enforces W^X on JIT memory pages.
	FlagSecure Flags = 16
	// FlagArgon2SSSE3 selects the SSSE3-optimized Argon2 for cache init.
	// Only honored if the library was compiled with SSSE3 support; this
	// build compiles the reference implementation only, so GetFlags never
	// reports it.
	FlagArgon2SSSE3 Flags = 32
	// FlagArgon2AVX2 selects the AVX2-optimized Argon2 (see FlagArgon2SSSE3).
	FlagArgon2AVX2 Flags = 64
	// FlagV2 selects the RandomX v2 algorithm revision. Chiral consensus
	// always sets this flag.
	FlagV2 Flags = 128
)

var (
	errCacheAlloc   = errors.New("randomx: cache allocation failed (out of memory, or large pages unavailable)")
	errDatasetAlloc = errors.New("randomx: dataset allocation failed (out of memory, or large pages unavailable)")
	errVMCreate     = errors.New("randomx: VM creation failed (invalid flag/cache/dataset combination, or out of memory)")
	errReleased     = errors.New("randomx: use after release")
)

// GetFlags returns the recommended flags for the current machine: FlagJIT
// where supported and FlagHardAES if the CPU has AES instructions. It never
// includes FlagLargePages, FlagFullMem, FlagSecure or FlagV2; callers add
// those as needed.
func GetFlags() Flags {
	return Flags(C.randomx_get_flags())
}

// Cache holds the ~256 MiB RandomX cache derived from an epoch key. It backs
// light-mode VMs and Dataset initialization.
type Cache struct {
	mu  sync.Mutex
	ptr *C.randomx_cache
}

// AllocCache allocates an uninitialized cache. The result must be initialized
// with Init before use and released with Release when done.
func AllocCache(flags Flags) (*Cache, error) {
	ptr := C.randomx_alloc_cache(C.randomx_flags(flags))
	if ptr == nil {
		return nil, errCacheAlloc
	}
	c := &Cache{ptr: ptr}
	runtime.SetFinalizer(c, (*Cache).Release)
	return c, nil
}

// Init (re)initializes the cache with the given key. Passing the key the
// cache is already initialized with is a cheap no-op. The key bytes are
// copied by the library and not retained.
func (c *Cache) Init(key []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ptr == nil {
		return errReleased
	}
	var kp unsafe.Pointer
	if len(key) > 0 {
		kp = unsafe.Pointer(&key[0])
	}
	C.randomx_init_cache(c.ptr, kp, C.size_t(len(key)))
	return nil
}

// Release frees the cache memory. Safe to call more than once. The caller
// must guarantee no VM or dataset initialization is still using the cache.
func (c *Cache) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ptr != nil {
		C.randomx_release_cache(c.ptr)
		c.ptr = nil
		runtime.SetFinalizer(c, nil)
	}
}

// Dataset holds the ~2.1 GiB RandomX dataset used by fast-mode (mining) VMs.
type Dataset struct {
	mu  sync.Mutex
	ptr *C.randomx_dataset
}

// DatasetItemCount returns the number of 64-byte items a full dataset
// initialization must cover.
func DatasetItemCount() uint64 {
	return uint64(C.randomx_dataset_item_count())
}

// AllocDataset allocates an uninitialized dataset. Only FlagLargePages is
// meaningful here.
func AllocDataset(flags Flags) (*Dataset, error) {
	ptr := C.randomx_alloc_dataset(C.randomx_flags(flags))
	if ptr == nil {
		return nil, errDatasetAlloc
	}
	d := &Dataset{ptr: ptr}
	runtime.SetFinalizer(d, (*Dataset).Release)
	return d, nil
}

// Init computes dataset items [startItem, startItem+itemCount) from the given
// initialized cache. Distinct item ranges may be initialized concurrently
// from multiple goroutines; the full range [0, DatasetItemCount()) must be
// covered before the dataset is used.
func (d *Dataset) Init(cache *Cache, startItem, itemCount uint64) error {
	if d.ptr == nil || cache.ptr == nil {
		return errReleased
	}
	C.randomx_init_dataset(d.ptr, cache.ptr, C.ulong(startItem), C.ulong(itemCount))
	runtime.KeepAlive(cache)
	return nil
}

// InitFull initializes the entire dataset from cache using the given number
// of goroutines (<= 0 means GOMAXPROCS). It returns once all items are done.
func (d *Dataset) InitFull(cache *Cache, threads int) error {
	if threads <= 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	total := DatasetItemCount()
	if uint64(threads) > total {
		threads = int(total)
	}
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	chunk := total / uint64(threads)
	for i := 0; i < threads; i++ {
		start := uint64(i) * chunk
		count := chunk
		if i == threads-1 {
			count = total - start
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Init(cache, start, count); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// Release frees the dataset memory. Safe to call more than once. The caller
// must guarantee no VM is still using the dataset.
func (d *Dataset) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ptr != nil {
		C.randomx_release_dataset(d.ptr)
		d.ptr = nil
		runtime.SetFinalizer(d, nil)
	}
}

// VM is a RandomX virtual machine. It is not safe for concurrent use; create
// one VM per hashing goroutine. VMs are cheap relative to caches/datasets
// (a few MiB of scratchpad).
type VM struct {
	ptr *C.randomx_vm
	// Keep the backing memory alive for the lifetime of the VM so a lost
	// reference cannot let a finalizer release memory the VM still uses.
	cache   *Cache
	dataset *Dataset
}

// CreateVM creates a VM.
//   - Light mode: flags without FlagFullMem, cache non-nil, dataset nil.
//   - Fast mode: flags with FlagFullMem, dataset non-nil (cache may be nil).
//
// The flags must match those used to allocate/init the cache and dataset
// (in particular FlagJIT, FlagHardAES, FlagLargePages and FlagV2).
func CreateVM(flags Flags, cache *Cache, dataset *Dataset) (*VM, error) {
	var cp *C.randomx_cache
	if cache != nil {
		if cache.ptr == nil {
			return nil, errReleased
		}
		cp = cache.ptr
	}
	var dp *C.randomx_dataset
	if dataset != nil {
		if dataset.ptr == nil {
			return nil, errReleased
		}
		dp = dataset.ptr
	}
	ptr := C.randomx_create_vm(C.randomx_flags(flags), cp, dp)
	if ptr == nil {
		return nil, errVMCreate
	}
	vm := &VM{ptr: ptr, cache: cache, dataset: dataset}
	runtime.SetFinalizer(vm, (*VM).Destroy)
	return vm, nil
}

// SetCache points a light-mode VM at a different (initialized) cache; used
// when the seed epoch changes.
func (vm *VM) SetCache(cache *Cache) error {
	if vm.ptr == nil || cache.ptr == nil {
		return errReleased
	}
	C.randomx_vm_set_cache(vm.ptr, cache.ptr)
	vm.cache = cache
	return nil
}

// SetDataset points a fast-mode VM at a different (initialized) dataset; used
// when the seed epoch changes.
func (vm *VM) SetDataset(dataset *Dataset) error {
	if vm.ptr == nil || dataset.ptr == nil {
		return errReleased
	}
	C.randomx_vm_set_dataset(vm.ptr, dataset.ptr)
	vm.dataset = dataset
	return nil
}

// CalcHash computes the RandomX hash of input.
func (vm *VM) CalcHash(input []byte) [HashSize]byte {
	var out [HashSize]byte
	var ip unsafe.Pointer
	if len(input) > 0 {
		ip = unsafe.Pointer(&input[0])
	}
	C.randomx_calculate_hash(vm.ptr, ip, C.size_t(len(input)), unsafe.Pointer(&out[0]))
	return out
}

// HashFirst begins a batched hash sequence with the first input. Use with
// HashNext/HashLast to overlap scratchpad initialization with program
// execution when hashing many inputs (mining loops).
func (vm *VM) HashFirst(input []byte) {
	var ip unsafe.Pointer
	if len(input) > 0 {
		ip = unsafe.Pointer(&input[0])
	}
	C.randomx_calculate_hash_first(vm.ptr, ip, C.size_t(len(input)))
}

// HashNext finishes the hash of the previous input and begins hashing the
// next one.
func (vm *VM) HashNext(nextInput []byte) [HashSize]byte {
	var out [HashSize]byte
	var ip unsafe.Pointer
	if len(nextInput) > 0 {
		ip = unsafe.Pointer(&nextInput[0])
	}
	C.randomx_calculate_hash_next(vm.ptr, ip, C.size_t(len(nextInput)), unsafe.Pointer(&out[0]))
	return out
}

// HashLast finishes the hash of the last queued input.
func (vm *VM) HashLast() [HashSize]byte {
	var out [HashSize]byte
	C.randomx_calculate_hash_last(vm.ptr, unsafe.Pointer(&out[0]))
	return out
}

// Destroy frees the VM. Safe to call more than once.
func (vm *VM) Destroy() {
	if vm.ptr != nil {
		C.randomx_destroy_vm(vm.ptr)
		vm.ptr = nil
		vm.cache = nil
		vm.dataset = nil
		runtime.SetFinalizer(vm, nil)
	}
}

// CalcCommitment computes the commitment hash binding an input to its
// RandomX hash (randomx_calculate_commitment).
func CalcCommitment(input []byte, hash [HashSize]byte) [HashSize]byte {
	var out [HashSize]byte
	var ip unsafe.Pointer
	if len(input) > 0 {
		ip = unsafe.Pointer(&input[0])
	}
	C.randomx_calculate_commitment(ip, C.size_t(len(input)), unsafe.Pointer(&hash[0]), unsafe.Pointer(&out[0]))
	return out
}
