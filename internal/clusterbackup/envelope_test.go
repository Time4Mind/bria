package clusterbackup_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
)

func TestEnvelopeRoundTripAndTamperDetection(t *testing.T) {
	envelope, err := clusterbackup.New(
		"cluster", "node", []byte(`{"state":"safe"}`), time.Unix(100, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Sign([]byte("certificate"), privateKey); err != nil {
		t.Fatal(err)
	}
	encoded, err := envelope.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := clusterbackup.Parse(encoded)
	if err != nil || string(decoded.Snapshot) != string(envelope.Snapshot) {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if err := decoded.VerifySignature(publicKey); err != nil {
		t.Fatal(err)
	}
	forged := decoded
	forged.CreatedAt = forged.CreatedAt.Add(time.Second)
	if err := forged.VerifySignature(publicKey); err == nil {
		t.Fatal("backup metadata tampering was accepted")
	}
	decoded.Snapshot[0] ^= 1
	if _, err := decoded.Marshal(); err == nil {
		t.Fatal("tampered backup was accepted")
	}
}
