package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const SignedManifestFormatVersion = 1

const signatureDomain = "bria.release-manifest.signature.v1\x00"

// TrustedKeys is the configured local trust root. The signed document contains
// only a key reference; public keys come from this independently provisioned
// map and private keys never enter verification composition.
type TrustedKeys map[string]ed25519.PublicKey

type signedManifestEnvelope struct {
	FormatVersion int             `json:"format_version"`
	KeyID         string          `json:"key_id"`
	Manifest      json.RawMessage `json:"manifest"`
	Signature     string          `json:"signature"`
}

// SignManifest produces a canonical envelope. The Ed25519 signature covers a
// domain separator, keyID, and the exact canonical manifest bytes, preventing
// a trusted-key reference from being substituted independently.
func SignManifest(manifest Manifest, keyID string, privateKey ed25519.PrivateKey) ([]byte, error) {
	if !validLabel(keyID, 128) {
		return nil, fmt.Errorf("%w: invalid signing key reference", ErrInvalidManifest)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, ErrInvalidSignature
	}
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(privateKey, signedPayload(keyID, manifestBytes))
	envelope := signedManifestEnvelope{
		FormatVersion: SignedManifestFormatVersion,
		KeyID:         keyID,
		Manifest:      manifestBytes,
		Signature:     base64.StdEncoding.EncodeToString(signature),
	}
	return json.Marshal(envelope)
}

// VerifySignedManifest resolves the exact key reference through trustedKeys,
// verifies the signature, and only then parses the canonical release manifest.
// It returns the verified key reference for an auditable installation receipt.
func VerifySignedManifest(envelopeBytes []byte, trustedKeys TrustedKeys) (Manifest, string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(envelopeBytes)))
	decoder.DisallowUnknownFields()
	var envelope signedManifestEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return Manifest{}, "", fmt.Errorf("%w: decode signed envelope: %v", ErrInvalidManifest, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF || envelope.FormatVersion != SignedManifestFormatVersion ||
		!validLabel(envelope.KeyID, 128) || len(envelope.Manifest) == 0 {
		return Manifest{}, "", fmt.Errorf("%w: invalid signed envelope", ErrInvalidManifest)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || base64.StdEncoding.EncodeToString(signature) != envelope.Signature {
		return Manifest{}, "", ErrInvalidSignature
	}
	publicKey, trusted := trustedKeys[envelope.KeyID]
	if !trusted {
		return Manifest{}, "", fmt.Errorf("%w: %s", ErrUntrustedKey, envelope.KeyID)
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, signedPayload(envelope.KeyID, envelope.Manifest), signature) {
		return Manifest{}, "", ErrInvalidSignature
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil || string(canonicalEnvelope) != string(envelopeBytes) {
		return Manifest{}, "", fmt.Errorf("%w: signed envelope is not canonical", ErrInvalidManifest)
	}
	manifest, err := parseCanonicalManifest(envelope.Manifest)
	if err != nil {
		return Manifest{}, "", err
	}
	return manifest, envelope.KeyID, nil
}

func signedPayload(keyID string, manifestBytes []byte) []byte {
	result := make([]byte, 0, len(signatureDomain)+len(keyID)+1+len(manifestBytes))
	result = append(result, signatureDomain...)
	result = append(result, keyID...)
	result = append(result, 0)
	result = append(result, manifestBytes...)
	return result
}
