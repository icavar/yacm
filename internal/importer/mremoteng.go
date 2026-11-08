// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/icavar/yacm/internal/store"
)

// mRemoteNG confCons.xml (public analysis: errno.fr/mRemoteNG.html,
// haseebT/mRemoteNG-Decrypt). Default master password "mR3m".
//   1.75+:  AES-GCM, key = PBKDF2-HMAC-SHA1(pw, salt, KdfIterations, 32),
//           base64(salt16 | nonce16 | ciphertext | tag16), AAD = salt
//   <=1.74: AES-CBC, key = MD5(pw), base64(iv16 | ciphertext)

const mrngDefaultPassword = "mR3m"

type mrngCrypto struct {
	password   string
	iterations int
	legacy     bool
	supported  bool
}

func (c mrngCrypto) decrypt(b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", errors.New("bad ciphertext")
	}
	if !c.supported {
		return "", errors.New("unsupported cipher")
	}
	if c.legacy {
		if len(data) < 32 || len(data)%16 != 0 {
			return "", errors.New("bad ciphertext")
		}
		key := md5.Sum([]byte(c.password))
		block, _ := aes.NewCipher(key[:])
		pt := make([]byte, len(data)-16)
		cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(pt, data[16:])
		pad := int(pt[len(pt)-1])
		if pad < 1 || pad > 16 || !bytes.Equal(pt[len(pt)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
			return "", errors.New("wrong password")
		}
		return string(pt[:len(pt)-pad]), nil
	}
	if len(data) < 16+16+16 {
		return "", errors.New("bad ciphertext")
	}
	salt, nonce, ct := data[:16], data[16:32], data[32:]
	key, err := pbkdf2.Key(sha1.New, c.password, salt, c.iterations, 32)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return "", err
	}
	pt, err := gcm.Open(nil, nonce, ct, salt)
	if err != nil {
		return "", errors.New("wrong password")
	}
	return string(pt), nil
}

type mrngNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []mrngNode `xml:",any"`
	Text     string     `xml:",chardata"`
}

func (n *mrngNode) attr(name string) string {
	for _, a := range n.Attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}

func importMRemoteNG(data []byte, parentID string, opts Options) (*Result, error) {
	var root mrngNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("not a valid mRemoteNG file: %w", err)
	}
	if !strings.EqualFold(root.XMLName.Local, "Connections") {
		return nil, errors.New("not an mRemoteNG connection file")
	}
	pw := opts.Password
	if pw == "" {
		pw = mrngDefaultPassword
	}
	c := mrngCrypto{password: pw, iterations: 1000, supported: true}
	if v, err := strconv.Atoi(root.attr("KdfIterations")); err == nil && v > 0 {
		c.iterations = v
	}
	engine, mode := strings.ToUpper(root.attr("EncryptionEngine")), strings.ToUpper(root.attr("BlockCipherMode"))
	if engine == "" && mode == "" {
		c.legacy = true // files from mRemoteNG 1.74 and older
	} else if engine != "AES" || mode != "GCM" {
		c.supported = false
	}
	b := newBuilder("mRemoteNG", parentID)
	if !c.supported {
		b.warn("", fmt.Sprintf("passwords use %s-%s, which YACM cannot decrypt yet; switch mRemoteNG to AES-GCM (Options > Security) and export again", engine, mode))
	}
	if p := root.attr("Protected"); p != "" && c.supported {
		if _, err := c.decrypt(p); err != nil {
			if opts.Password == "" {
				b.warn("", "this file is protected with a custom mRemoteNG password; enter it to import passwords")
			} else {
				b.warn("", "the mRemoteNG password is not correct")
			}
			c.supported = false
		}
	}
	nodes := root.Children
	if strings.EqualFold(root.attr("FullFileEncryption"), "true") {
		plain, err := c.decrypt(root.Text)
		if err != nil {
			return nil, errors.New("this mRemoteNG file is fully encrypted; enter the mRemoteNG password to import it")
		}
		var inner mrngNode
		if err := xml.Unmarshal([]byte("<Root>"+plain+"</Root>"), &inner); err != nil {
			return nil, fmt.Errorf("decrypted mRemoteNG content is not valid XML: %w", err)
		}
		nodes = inner.Children
	}
	var walk func(list []mrngNode, path []string)
	walk = func(list []mrngNode, path []string) {
		for i := range list {
			n := &list[i]
			if !strings.EqualFold(n.XMLName.Local, "Node") {
				continue
			}
			name := n.attr("Name")
			if strings.EqualFold(n.attr("Type"), "Container") {
				walk(n.Children, append(append([]string{}, path...), name))
				continue
			}
			mrngConnection(b, path, n, c)
		}
	}
	walk(nodes, nil)
	return b.done(), nil
}

func mrngConnection(b *builder, path []string, n *mrngNode, c mrngCrypto) {
	name := n.attr("Name")
	proto := strings.ToUpper(n.attr("Protocol"))
	node := &store.Node{ID: store.NewID(), Name: name, Description: n.attr("Descr")}
	ss := &store.Session{Host: n.attr("Hostname"), User: n.attr("Username")}
	ss.Port, _ = strconv.Atoi(n.attr("Port"))
	switch proto {
	case "SSH2", "SSH1":
		ss.Protocol, ss.Algorithms = store.ProtoSSH, store.AlgoCompatible
	case "TELNET":
		ss.Protocol = store.ProtoTelnet
	default:
		b.skip(name, n.attr("Protocol"), "only SSH, Telnet and serial sessions are supported")
		return
	}
	if d := n.attr("Domain"); d != "" && ss.User != "" && ss.Protocol == store.ProtoSSH {
		b.warn(name, "the Windows domain \""+d+"\" was not added to the user name")
	}
	pw := ""
	if enc := n.attr("Password"); enc != "" {
		if p, err := c.decrypt(enc); err == nil {
			pw = p
		} else {
			b.failed()
		}
	}
	if ss.Protocol == store.ProtoSSH {
		ss.Auth = store.AuthAsk
		if pw != "" {
			ss.Auth = store.AuthPassword
		}
		if ps := n.attr("PuttySession"); ps != "" && !strings.EqualFold(ps, "Default Settings") {
			b.warn(name, "uses the PuTTY session \""+ps+"\" for extra settings (keys, proxies); import PuTTY sessions as well and compare")
		}
	}
	node.Session = ss
	if b.add(path, node) {
		b.secret(store.SecretPassword(node.ID), pw)
	}
}
