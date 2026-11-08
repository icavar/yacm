// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"bytes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/blowfish"
)

// Ásbrú / PAC Manager "encrypt" every stored password with Crypt::CBC,
// Blowfish, a fixed passphrase compiled into the program and a fixed salt
// (pack('Q', 12345678)), using OpenSSL v1 key derivation (EVP_BytesToKey,
// MD5, one round). Anyone with the config file can therefore recover the
// passwords. We decrypt them once during import and re-store them in the
// real vault.
const asbruPassphrase = "PAC Manager (David Torrejon Vaquerizas, david.tv@gmail.com)"

// evpBytesToKey implements OpenSSL's legacy EVP_BytesToKey with MD5.
func evpBytesToKey(pass, salt []byte, keyLen, ivLen int) (key, iv []byte) {
	var out, prev []byte
	for len(out) < keyLen+ivLen {
		h := md5.New()
		h.Write(prev)
		h.Write(pass)
		h.Write(salt)
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:keyLen], out[keyLen : keyLen+ivLen]
}

// decryptAsbru decrypts a Crypt::CBC hex blob ("Salted__" header) produced by
// Ásbrú. It returns the input unchanged when it is not such a blob (Ásbrú
// leaves some fields, e.g. jump passwords, in clear text).
func decryptAsbru(hexStr string) (string, error) {
	hexStr = strings.TrimSpace(hexStr)
	if hexStr == "" {
		return "", nil
	}
	raw, err := hex.DecodeString(hexStr)
	if err != nil || len(raw) < 16 || !bytes.HasPrefix(raw, []byte("Salted__")) {
		return hexStr, errNotEncrypted
	}
	salt, ct := raw[8:16], raw[16:]
	if len(ct) == 0 || len(ct)%blowfish.BlockSize != 0 {
		return "", errors.New("ciphertext length is not a multiple of the block size")
	}
	// Crypt::CBC derives a 56-byte Blowfish key (Crypt::Blowfish keysize).
	key, iv := evpBytesToKey([]byte(asbruPassphrase), salt, 56, blowfish.BlockSize)
	block, err := blowfish.NewCipher(key)
	if err != nil {
		return "", err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > blowfish.BlockSize || pad > len(pt) {
		return "", errors.New("bad padding")
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return "", errors.New("bad padding")
		}
	}
	pt = pt[:len(pt)-pad]
	if !utf8.Valid(pt) {
		return "", errors.New("decrypted value is not valid UTF-8")
	}
	return string(pt), nil
}

var errNotEncrypted = errors.New("value is not encrypted")
