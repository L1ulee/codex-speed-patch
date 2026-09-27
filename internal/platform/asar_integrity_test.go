package platform

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestIntegrityDictionaryDigestMatchesOfficialBundle(t *testing.T) {
	entries := map[string]asarIntegrityEntry{
		"Resources/app.asar": {
			Algorithm: "SHA256",
			Hash:      "cdfebc59f4a32f4202b12bc26bbc0ff6875fec9491eb2254a5fc0b75eae48d2e",
		},
	}
	digest, err := integrityDictionaryDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(digest[:]), "0f8538ec9f471984725fdf0f8a172b730176a44f8b2da515c2c7a4ff6a144028"; got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
}

func TestDecodeIntegritySlot(t *testing.T) {
	slot := make([]byte, 66)
	copy(slot, integritySlotSentinel)
	slot[32], slot[33] = 1, 1
	copy(slot[34:], bytes.Repeat([]byte{0x42}, 32))
	digest, err := decodeIntegritySlot(slot)
	if err != nil || !bytes.Equal(digest[:], bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatalf("decodeIntegritySlot() = %x, %v", digest, err)
	}
	for _, change := range []func([]byte){
		func(data []byte) { data[0] = 0 },
		func(data []byte) { data[32] = 0 },
		func(data []byte) { data[33] = 2 },
	} {
		invalid := append([]byte(nil), slot...)
		change(invalid)
		if _, err := decodeIntegritySlot(invalid); err == nil {
			t.Fatal("invalid integrity slot was accepted")
		}
	}
}
