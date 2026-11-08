// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package ppk reads PuTTY private key files (.ppk, format versions 2 and 3),
// so keys made with PuTTYgen work without converting them first.
//
// Format (PuTTY documentation, appendix C "PPK file format"):
//
//	PuTTY-User-Key-File-<2|3>: <algorithm>
//	Encryption: none | aes256-cbc
//	Comment: <text>
//	Public-Lines: <n>       base64 public key blob follows
//	Key-Derivation: Argon2id | Argon2i | Argon2d   (version 3, encrypted only)
//	Argon2-Memory / Argon2-Passes / Argon2-Parallelism / Argon2-Salt
//	Private-Lines: <n>      base64 private blob follows (encrypted if set)
//	Private-MAC: <hex>
//
// Version 2 derives the AES key from SHA-1 of the passphrase and protects the
// file with HMAC-SHA-1; version 3 derives cipher key, IV and MAC key with
// Argon2 and uses HMAC-SHA-256.
package ppk

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/dsa" //nolint:staticcheck // PuTTY still writes DSA keys
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	// ErrEncrypted means the key needs a passphrase.
	ErrEncrypted = errors.New("ppk: key is encrypted")
	// ErrBadPassphrase means the passphrase is wrong (or the file damaged).
	ErrBadPassphrase = errors.New("ppk: wrong passphrase or damaged key file")
)

// IsPPK reports whether data looks like a PuTTY key file.
func IsPPK(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, "\ufeff \t\r\n"), []byte("PuTTY-User-Key-File-"))
}

// Encrypted reports whether a PuTTY key file needs a passphrase.
func Encrypted(data []byte) bool {
	f, err := parseFile(data)
	return err == nil && f.encryption != "none"
}

type file struct {
	version    int
	algorithm  string
	encryption string
	comment    string
	public     []byte
	private    []byte
	mac        []byte
	kdf        string
	memory     uint32
	passes     uint32
	threads    uint8
	salt       []byte
}

// Parse decodes a PuTTY key file and returns the private key as
// *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey or *dsa.PrivateKey.
// An encrypted key without passphrase returns ErrEncrypted.
func Parse(data []byte, passphrase []byte) (any, error) {
	f, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	if f.encryption != "none" && len(passphrase) == 0 {
		return nil, ErrEncrypted
	}
	if f.encryption == "none" {
		passphrase = nil
	}
	priv, err := f.decrypt(passphrase)
	if err != nil {
		return nil, err
	}
	return f.key(priv)
}

// Comment returns the key comment, or "".
func Comment(data []byte) string {
	f, err := parseFile(data)
	if err != nil {
		return ""
	}
	return f.comment
}

func parseFile(data []byte) (*file, error) {
	data = bytes.TrimLeft(data, "\ufeff")
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	f := &file{}
	header := func() (string, string, error) {
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			k, v, ok := strings.Cut(line, ": ")
			if !ok {
				return "", "", fmt.Errorf("ppk: bad line %q", line)
			}
			return k, v, nil
		}
		if err := sc.Err(); err != nil {
			return "", "", err
		}
		return "", "", errors.New("ppk: file ends too early")
	}
	lines := func(n string) ([]byte, error) {
		count, err := strconv.Atoi(n)
		if err != nil || count < 0 || count > 10000 {
			return nil, fmt.Errorf("ppk: bad line count %q", n)
		}
		var b strings.Builder
		for i := 0; i < count; i++ {
			if !sc.Scan() {
				return nil, errors.New("ppk: file ends too early")
			}
			b.WriteString(strings.TrimSpace(sc.Text()))
		}
		return base64.StdEncoding.DecodeString(b.String())
	}

	k, v, err := header()
	if err != nil {
		return nil, err
	}
	switch k {
	case "PuTTY-User-Key-File-2":
		f.version = 2
	case "PuTTY-User-Key-File-3":
		f.version = 3
	case "PuTTY-User-Key-File-1":
		return nil, errors.New("ppk: version 1 key files are not supported; load and save the key again in PuTTYgen")
	default:
		return nil, errors.New("ppk: not a PuTTY key file")
	}
	f.algorithm = v
	for {
		k, v, err := header()
		if err != nil {
			return nil, err
		}
		switch k {
		case "Encryption":
			f.encryption = v
		case "Comment":
			f.comment = v
		case "Public-Lines":
			if f.public, err = lines(v); err != nil {
				return nil, err
			}
		case "Key-Derivation":
			f.kdf = v
		case "Argon2-Memory", "Argon2-Passes", "Argon2-Parallelism":
			n, perr := strconv.ParseUint(v, 10, 32)
			if perr != nil {
				return nil, fmt.Errorf("ppk: bad %s", k)
			}
			switch k {
			case "Argon2-Memory":
				f.memory = uint32(n)
			case "Argon2-Passes":
				f.passes = uint32(n)
			default:
				if n == 0 || n > 255 {
					return nil, errors.New("ppk: bad Argon2-Parallelism")
				}
				f.threads = uint8(n)
			}
		case "Argon2-Salt":
			if f.salt, err = hex.DecodeString(v); err != nil {
				return nil, errors.New("ppk: bad Argon2-Salt")
			}
		case "Private-Lines":
			if f.private, err = lines(v); err != nil {
				return nil, err
			}
		case "Private-MAC":
			if f.mac, err = hex.DecodeString(v); err != nil {
				return nil, errors.New("ppk: bad Private-MAC")
			}
			if f.public == nil || f.private == nil {
				return nil, errors.New("ppk: key data missing")
			}
			switch f.encryption {
			case "none", "aes256-cbc":
			default:
				return nil, fmt.Errorf("ppk: unsupported encryption %q", f.encryption)
			}
			return f, nil
		case "Private-Hash":
			return nil, errors.New("ppk: old key file format; load and save the key again in PuTTYgen")
		}
	}
}

// decrypt returns the private blob after checking the MAC.
func (f *file) decrypt(pass []byte) ([]byte, error) {
	var cipherKey, iv, macKey []byte
	var newMAC func() hash.Hash
	switch f.version {
	case 2:
		newMAC = sha1.New
		h := sha1.New()
		h.Write([]byte("putty-private-key-file-mac-key"))
		h.Write(pass)
		macKey = h.Sum(nil)
		if f.encryption != "none" {
			k := make([]byte, 0, 40)
			for i := uint32(0); i < 2; i++ {
				h := sha1.New()
				binary.Write(h, binary.BigEndian, i)
				h.Write(pass)
				k = h.Sum(k)
			}
			cipherKey, iv = k[:32], make([]byte, aes.BlockSize)
		}
	case 3:
		newMAC = sha256.New
		if f.encryption != "none" {
			// A crafted key file could ask for gigabytes and minutes of work and
			// hang YACM: at most 1 GiB and 16 GiB-passes of work (PuTTYgen uses
			// 8 MiB and a few dozen passes).
			if f.memory == 0 || f.memory > 1024*1024 || f.passes == 0 || f.passes > 1000 || uint64(f.memory)*uint64(f.passes) > 16*1024*1024 || f.threads == 0 || len(f.salt) == 0 {
				return nil, errors.New("ppk: bad Argon2 parameters")
			}
			var out []byte
			switch f.kdf {
			case "Argon2id":
				out = argon2.IDKey(pass, f.salt, f.passes, f.memory, f.threads, 80)
			case "Argon2i":
				out = argon2.Key(pass, f.salt, f.passes, f.memory, f.threads, 80)
			default:
				return nil, fmt.Errorf("ppk: key derivation %q is not supported; save the key again in PuTTYgen with Argon2id", f.kdf)
			}
			cipherKey, iv, macKey = out[:32], out[32:48], out[48:80]
		} else {
			macKey = []byte{}
		}
	}

	priv := f.private
	if cipherKey != nil {
		if len(priv) == 0 || len(priv)%aes.BlockSize != 0 {
			return nil, ErrBadPassphrase
		}
		block, _ := aes.NewCipher(cipherKey)
		plain := make([]byte, len(priv))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, priv)
		priv = plain
	}

	mac := hmac.New(newMAC, macKey)
	for _, part := range [][]byte{[]byte(f.algorithm), []byte(f.encryption), []byte(f.comment), f.public, priv} {
		binary.Write(mac, binary.BigEndian, uint32(len(part)))
		mac.Write(part)
	}
	if !hmac.Equal(mac.Sum(nil), f.mac) {
		if f.encryption != "none" {
			return nil, ErrBadPassphrase
		}
		return nil, errors.New("ppk: key file is damaged (MAC does not match)")
	}
	return priv, nil
}

// reader walks SSH wire-format fields.
type reader struct {
	b   []byte
	err error
}

func (r *reader) str() []byte {
	if r.err != nil {
		return nil
	}
	if len(r.b) < 4 {
		r.err = errors.New("ppk: key data too short")
		return nil
	}
	n := binary.BigEndian.Uint32(r.b)
	if uint64(n) > uint64(len(r.b)-4) {
		r.err = errors.New("ppk: key data too short")
		return nil
	}
	v := r.b[4 : 4+n]
	r.b = r.b[4+n:]
	return v
}

func (r *reader) mpint() *big.Int {
	v := r.str()
	if r.err != nil {
		return nil
	}
	if len(v) > 0 && v[0]&0x80 != 0 {
		r.err = errors.New("ppk: negative number in key")
		return nil
	}
	return new(big.Int).SetBytes(v)
}

func (f *file) key(priv []byte) (any, error) {
	pub := &reader{b: f.public}
	pr := &reader{b: priv}
	if alg := string(pub.str()); pub.err == nil && alg != f.algorithm {
		return nil, errors.New("ppk: public key does not match the header")
	}
	switch f.algorithm {
	case "ssh-rsa":
		e, n := pub.mpint(), pub.mpint()
		d, p, q, _ := pr.mpint(), pr.mpint(), pr.mpint(), pr.mpint()
		if err := errors.Join(pub.err, pr.err); err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("ppk: unsupported RSA exponent")
		}
		k := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: n, E: int(e.Int64())}, D: d, Primes: []*big.Int{p, q}}
		if err := k.Validate(); err != nil {
			return nil, fmt.Errorf("ppk: invalid RSA key: %w", err)
		}
		k.Precompute()
		return k, nil
	case "ssh-dss":
		p, q, g, y := pub.mpint(), pub.mpint(), pub.mpint(), pub.mpint()
		x := pr.mpint()
		if err := errors.Join(pub.err, pr.err); err != nil {
			return nil, err
		}
		return &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: dsa.Parameters{P: p, Q: q, G: g}, Y: y}, X: x}, nil
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		curveName := string(pub.str())
		point := pub.str()
		d := pr.mpint()
		if err := errors.Join(pub.err, pr.err); err != nil {
			return nil, err
		}
		var curve elliptic.Curve
		switch curveName {
		case "nistp256":
			curve = elliptic.P256()
		case "nistp384":
			curve = elliptic.P384()
		case "nistp521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("ppk: unknown curve %q", curveName)
		}
		x, y := elliptic.Unmarshal(curve, point) //nolint:staticcheck // plain point decoding
		if x == nil {
			return nil, errors.New("ppk: bad ECDSA public point")
		}
		k := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}
		if gx, gy := curve.ScalarBaseMult(d.Bytes()); gx.Cmp(x) != 0 || gy.Cmp(y) != 0 {
			return nil, errors.New("ppk: ECDSA private key does not match the public key")
		}
		return k, nil
	case "ssh-ed25519":
		pk := pub.str()
		seed := pr.str()
		if err := errors.Join(pub.err, pr.err); err != nil {
			return nil, err
		}
		if len(pk) != ed25519.PublicKeySize || len(seed) != ed25519.SeedSize {
			return nil, errors.New("ppk: bad Ed25519 key length")
		}
		k := ed25519.NewKeyFromSeed(seed)
		if !bytes.Equal(k.Public().(ed25519.PublicKey), pk) {
			return nil, errors.New("ppk: Ed25519 private key does not match the public key")
		}
		return k, nil
	default:
		return nil, fmt.Errorf("ppk: %s keys are not supported", f.algorithm)
	}
}
