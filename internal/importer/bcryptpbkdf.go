// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"crypto/sha512"

	"golang.org/x/crypto/blowfish"
)

// bcryptPBKDF is OpenBSD's bcrypt_pbkdf (as used by OpenSSH key files and
// SecureCRT "03:" passwords). Unlike some libraries it accepts an empty
// password, which SecureCRT uses when no config passphrase is set.
func bcryptPBKDF(password, salt []byte, rounds, keyLen int) []byte {
	const blockSize = 32
	numBlocks := (keyLen + blockSize - 1) / blockSize
	key := make([]byte, numBlocks*blockSize)
	h := sha512.New()
	h.Write(password)
	shapass := h.Sum(nil)
	shasalt := make([]byte, 0, sha512.Size)
	cnt, tmp := make([]byte, 4), make([]byte, blockSize)
	for block := 1; block <= numBlocks; block++ {
		h.Reset()
		h.Write(salt)
		cnt[0], cnt[1], cnt[2], cnt[3] = byte(block>>24), byte(block>>16), byte(block>>8), byte(block)
		h.Write(cnt)
		bcryptHash(tmp, shapass, h.Sum(shasalt))
		out := make([]byte, blockSize)
		copy(out, tmp)
		for i := 2; i <= rounds; i++ {
			h.Reset()
			h.Write(tmp)
			bcryptHash(tmp, shapass, h.Sum(shasalt))
			for j := range out {
				out[j] ^= tmp[j]
			}
		}
		for i, v := range out {
			key[i*numBlocks+(block-1)] = v
		}
	}
	return key[:keyLen]
}

var bcryptMagic = []byte("OxychromaticBlowfishSwatDynamite")

func bcryptHash(out, shapass, shasalt []byte) {
	c, err := blowfish.NewSaltedCipher(shapass, shasalt)
	if err != nil {
		panic(err)
	}
	for i := 0; i < 64; i++ {
		blowfish.ExpandKey(shasalt, c)
		blowfish.ExpandKey(shapass, c)
	}
	copy(out, bcryptMagic)
	for i := 0; i < 32; i += 8 {
		for j := 0; j < 64; j++ {
			c.Encrypt(out[i:i+8], out[i:i+8])
		}
	}
	for i := 0; i < 32; i += 4 {
		out[i+3], out[i+2], out[i+1], out[i] = out[i], out[i+1], out[i+2], out[i+3]
	}
}
