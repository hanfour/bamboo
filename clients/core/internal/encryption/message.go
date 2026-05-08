// SPDX-License-Identifier: AGPL-3.0-or-later
//
// origin: netbirdio/netbird@7da94a4956af76f7187733aa488e9d20a0f62202
// upstream path: encryption/message.go
//
// Local changes vs upstream:
//   - log/slog replaces github.com/sirupsen/logrus
//   - google.golang.org/protobuf/proto replaces github.com/golang/protobuf/proto
//     (the legacy package is deprecated; the new API is wire-compatible)

package encryption

import (
	"log/slog"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/protobuf/proto"
)

// EncryptMessage marshals message and encrypts it for remotePubKey.
func EncryptMessage(remotePubKey wgtypes.Key, ourPrivateKey wgtypes.Key, message proto.Message) ([]byte, error) {
	body, err := proto.Marshal(message)
	if err != nil {
		slog.Error("marshal proto message", "err", err)
		return nil, err
	}
	encrypted, err := Encrypt(body, remotePubKey, ourPrivateKey)
	if err != nil {
		slog.Error("encrypt proto message", "err", err)
		return nil, err
	}
	return encrypted, nil
}

// DecryptMessage decrypts encryptedMessage and unmarshals into message.
func DecryptMessage(remotePubKey wgtypes.Key, ourPrivateKey wgtypes.Key, encryptedMessage []byte, message proto.Message) error {
	decrypted, err := Decrypt(encryptedMessage, remotePubKey, ourPrivateKey)
	if err != nil {
		slog.Warn("decrypt proto message", "peer", remotePubKey.String(), "err", err)
		return err
	}
	if err := proto.Unmarshal(decrypted, message); err != nil {
		slog.Warn("unmarshal proto message", "peer", remotePubKey.String(), "err", err)
		return err
	}
	return nil
}
