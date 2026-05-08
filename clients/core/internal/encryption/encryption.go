// SPDX-License-Identifier: AGPL-3.0-or-later
//
// origin: netbirdio/netbird@7da94a4956af76f7187733aa488e9d20a0f62202
// upstream path: encryption/encryption.go

// Package encryption provides authenticated encryption between two peers
// using their WireGuard Curve25519 key pairs. The wire format is NaCl
// box: a 24-byte random nonce followed by an XSalsa20+Poly1305 sealed
// message. The local private key + remote public key form the shared
// secret.
package encryption

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/nacl/box"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const nonceSize = 24

// Encrypt encrypts msg for the holder of peerPublicKey using privateKey
// as the local secret. The returned slice contains the nonce followed
// by the sealed message.
func Encrypt(msg []byte, peerPublicKey wgtypes.Key, privateKey wgtypes.Key) ([]byte, error) {
	nonce, err := genNonce()
	if err != nil {
		return nil, err
	}
	return box.Seal(nonce[:], msg, nonce, toByte32(peerPublicKey), toByte32(privateKey)), nil
}

// Decrypt decrypts a payload produced by Encrypt for our key pair.
// peerPublicKey is the sender's public key.
func Decrypt(encryptedMsg []byte, peerPublicKey wgtypes.Key, privateKey wgtypes.Key) ([]byte, error) {
	if len(encryptedMsg) < nonceSize {
		return nil, fmt.Errorf("invalid encrypted message length")
	}
	var nonce [nonceSize]byte
	copy(nonce[:], encryptedMsg[:nonceSize])
	opened, ok := box.Open(nil, encryptedMsg[nonceSize:], &nonce, toByte32(peerPublicKey), toByte32(privateKey))
	if !ok {
		return nil, fmt.Errorf("failed to decrypt message from peer %s", peerPublicKey.String())
	}
	return opened, nil
}

// genNonce returns a random 24-byte nonce.
func genNonce() (*[nonceSize]byte, error) {
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &nonce, nil
}

// toByte32 reinterprets a wgtypes.Key as the *[32]byte expected by NaCl box.
func toByte32(key wgtypes.Key) *[32]byte {
	return (*[32]byte)(&key)
}
