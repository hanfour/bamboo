// SPDX-License-Identifier: AGPL-3.0-or-later
//
// origin: netbirdio/netbird@7da94a4956af76f7187733aa488e9d20a0f62202
// upstream path: base62/base62.go
//
// This file was imported from upstream NetBird per the inventory at
// docs/development/netbird-import-inventory.md. It remains licensed
// under AGPLv3 until clean-room rewrite per ADR 0011.

// Package base62 provides Encode/Decode helpers for uint32 ↔ base62 strings.
package base62

import (
	"fmt"
	"math"
	"strings"
)

const (
	alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base     = uint32(len(alphabet))
)

// Encode encodes a uint32 value to a base62 string.
func Encode(num uint32) string {
	if num == 0 {
		return string(alphabet[0])
	}

	var encoded strings.Builder

	for num > 0 {
		remainder := num % base
		encoded.WriteByte(alphabet[remainder])
		num /= base
	}

	// Reverse the encoded string
	encodedString := encoded.String()
	reversed := reverse(encodedString)
	return reversed
}

// Decode decodes a base62 string to a uint32 value.
func Decode(encoded string) (uint32, error) {
	var decoded uint32
	strLen := len(encoded)

	for i, char := range encoded {
		index := strings.IndexRune(alphabet, char)
		if index < 0 {
			return 0, fmt.Errorf("invalid character: %c", char)
		}

		decoded += uint32(index) * uint32(math.Pow(float64(base), float64(strLen-i-1)))
	}

	return decoded, nil
}

// Reverse a string.
func reverse(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
