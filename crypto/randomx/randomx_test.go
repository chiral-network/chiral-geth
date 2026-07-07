package randomx

import (
	"bytes"
	"encoding/hex"
	"runtime"
	"testing"
)

// Chiral RandomX v2 test vectors: the upstream test inputs from
// librandomx/src/tests/tests.cpp hashed under Chiral's unique
// RANDOMX_ARGON_SALT ("RandomX-Chiral\x01"; miner algo id rx/chiral).
//
// Provenance: the vendored library at the pinned commit first reproduced the
// official stock-configuration v2 vectors bit-exactly (interpreter, JIT and
// full-dataset modes; commit 888d0ae72), then the salt was applied and these
// values were derived with that validated build. Regenerate the same way
// after any configuration.h change (see VENDOR.md).
var vectorsV2 = []struct {
	key   string
	input string // raw string, or hex when isHex is set
	isHex bool
	want  string
}{
	{"test key 000", "This is a test", false,
		"456272039d3d4caf7f5c39a7850d364dab4debe74a97c200a439318b858f6acd"},
	{"test key 000", "Lorem ipsum dolor sit amet", false,
		"54423d119c4127dffe6bc5b8d95177ecb22ba589034d875b2c63c77efcb872ac"},
	{"test key 000", "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua", false,
		"b4a9bc61ab6e72288b76abec0915f512e48c106425affb03db84e5651fcdf978"},
	{"test key 001", "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua", false,
		"8b21286612b37b16febbf0dffda7e2d0fb0d99ec7ad21db641ae61e136bdc7b1"},
	{"test key 001", "0b0b98bea7e805e0010a2126d287a2a0cc833d312cb786385a7c2f9de69d25537f584a9bc9977b00000000666fd8753bf61a8631f12984e3fd44f4014eca629276817b56f32e9b68bd82f416", true,
		"3eb4c3d63379a9f35e8663de795ff193509f79f9ed8aaa33b3eb798677ce2c61"},
}

func vectorInput(t *testing.T, v struct {
	key   string
	input string
	isHex bool
	want  string
}) []byte {
	if !v.isHex {
		return []byte(v.input)
	}
	b, err := hex.DecodeString(v.input)
	if err != nil {
		t.Fatalf("bad test vector hex: %v", err)
	}
	return b
}

// runVectors checks all v2 vectors in light mode under the given flags,
// re-initializing the shared cache when the key changes (also exercising the
// same-key re-init fast path).
func runVectors(t *testing.T, flags Flags) {
	cache, err := AllocCache(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Release()

	var curKey string
	vm, err := (*VM)(nil), error(nil)
	for i, v := range vectorsV2 {
		if vm == nil || v.key != curKey {
			if err = cache.Init([]byte(v.key)); err != nil {
				t.Fatal(err)
			}
			curKey = v.key
			if vm == nil {
				if vm, err = CreateVM(flags, cache, nil); err != nil {
					t.Fatal(err)
				}
				defer vm.Destroy()
			} else if err = vm.SetCache(cache); err != nil {
				t.Fatal(err)
			}
		}
		got := vm.CalcHash(vectorInput(t, v))
		want, _ := hex.DecodeString(v.want)
		if !bytes.Equal(got[:], want) {
			t.Errorf("vector %d (key %q): got %x, want %s", i, v.key, got, v.want)
		}
	}
}

func TestVectorsV2LightInterpreter(t *testing.T) {
	runVectors(t, FlagV2)
}

func TestVectorsV2LightJIT(t *testing.T) {
	if GetFlags()&FlagJIT == 0 {
		t.Skip("JIT not supported on this platform")
	}
	runVectors(t, FlagV2|FlagJIT|FlagSecure|(GetFlags()&FlagHardAES))
}

func TestBatchHashV2(t *testing.T) {
	flags := FlagV2 | (GetFlags() & (FlagJIT | FlagHardAES))
	if flags&FlagJIT != 0 {
		flags |= FlagSecure
	}
	cache, err := AllocCache(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Release()
	if err := cache.Init([]byte("test key 000")); err != nil {
		t.Fatal(err)
	}
	vm, err := CreateVM(flags, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()

	// Batched hashing must produce the same hashes as one-shot CalcHash
	// (vectors 0..2 share "test key 000").
	vm.HashFirst(vectorInput(t, vectorsV2[0]))
	h0 := vm.HashNext(vectorInput(t, vectorsV2[1]))
	h1 := vm.HashNext(vectorInput(t, vectorsV2[2]))
	h2 := vm.HashLast()
	for i, got := range [][HashSize]byte{h0, h1, h2} {
		want, _ := hex.DecodeString(vectorsV2[i].want)
		if !bytes.Equal(got[:], want) {
			t.Errorf("batched hash %d: got %x, want %s", i, got, vectorsV2[i].want)
		}
	}
}

// TestVectorsV2FullMem proves light/fast mode equivalence: the first vector
// must produce the identical hash when computed against a full dataset.
// Requires ~2.4 GiB of memory; skipped in -short mode.
func TestVectorsV2FullMem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 2 GiB dataset test in short mode")
	}
	flags := FlagV2 | (GetFlags() & (FlagJIT | FlagHardAES))
	if flags&FlagJIT != 0 {
		flags |= FlagSecure
	}
	cache, err := AllocCache(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Release()
	if err := cache.Init([]byte(vectorsV2[0].key)); err != nil {
		t.Fatal(err)
	}
	dataset, err := AllocDataset(flags)
	if err != nil {
		t.Skipf("dataset allocation failed (likely insufficient memory): %v", err)
	}
	defer dataset.Release()
	if err := dataset.InitFull(cache, runtime.GOMAXPROCS(0)); err != nil {
		t.Fatal(err)
	}
	vm, err := CreateVM(flags|FlagFullMem, nil, dataset)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()

	got := vm.CalcHash(vectorInput(t, vectorsV2[0]))
	want, _ := hex.DecodeString(vectorsV2[0].want)
	if !bytes.Equal(got[:], want) {
		t.Errorf("full-mem hash: got %x, want %s", got, vectorsV2[0].want)
	}
}

func TestDatasetItemCount(t *testing.T) {
	// 2 GiB base + 32 MiB extra, in 64-byte items (stock configuration.h).
	const want = (2147483648 + 33554368) / 64
	if got := DatasetItemCount(); got != want {
		t.Errorf("DatasetItemCount: got %d, want %d", got, want)
	}
}
