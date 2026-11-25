package mega

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// MEGA cryptography: every value travels as unpadded base64url; keys are
// sequences of big-endian uint32 words ("a32" in the web client).

// ErrMACMismatch is returned by VerifyMAC when the content does not match
// the meta-MAC embedded in the node key.
var ErrMACMismatch = errors.New("mega: MAC mismatch (file corrupted or wrong key)")

var errBadKey = errors.New("mega: invalid key")

// b64Decode decodes MEGA base64url (padding, '+' and '/' tolerated).
func b64Decode(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_", ",", "").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

// b64Encode encodes like MEGA (base64url, no padding).
func b64Encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fileKey is an unpacked 256-bit MEGA file key.
type fileKey struct {
	AES     [16]byte // AES-128 key (both halves XOR-folded)
	Nonce   [8]byte  // CTR nonce (high 64 bits of the counter block)
	MetaMAC [8]byte  // condensed MAC of the plaintext
}

// unpackFileKey folds a 32-byte file key into AES key, nonce and meta-MAC.
func unpackFileKey(raw []byte) (fileKey, error) {
	var k fileKey
	if len(raw) != 32 {
		return k, fmt.Errorf("%w: file key has %d bytes, want 32", errBadKey, len(raw))
	}
	for i := 0; i < 16; i++ {
		k.AES[i] = raw[i] ^ raw[i+16]
	}
	copy(k.Nonce[:], raw[16:24])
	copy(k.MetaMAC[:], raw[24:32])
	return k, nil
}

// parseFileKey decodes and unpacks a base64url file key.
func parseFileKey(s string) (fileKey, error) {
	raw, err := b64Decode(s)
	if err != nil {
		return fileKey{}, fmt.Errorf("%w: %v", errBadKey, err)
	}
	return unpackFileKey(raw)
}

// attrKey returns the AES key used for node attributes: the folded key for
// files (32 bytes) and the key itself for folders (16 bytes).
func attrKey(raw []byte) ([]byte, error) {
	switch len(raw) {
	case 16:
		return raw, nil
	case 32:
		k, _ := unpackFileKey(raw)
		return k.AES[:], nil
	}
	return nil, fmt.Errorf("%w: node key has %d bytes", errBadKey, len(raw))
}

// nodeAttrs is the decrypted attribute JSON ("n" = name).
type nodeAttrs struct {
	Name string `json:"n"`
}

// decryptAttrs decrypts a node attribute blob (AES-CBC, zero IV, "MEGA{json}"
// zero-padded) with a 16-byte key. It never panics on malformed input.
func decryptAttrs(key []byte, enc string) (nodeAttrs, error) {
	var a nodeAttrs
	data, err := b64Decode(enc)
	if err != nil {
		return a, fmt.Errorf("mega: attributes: %w", err)
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return a, errors.New("mega: attributes: bad length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return a, fmt.Errorf("mega: attributes: %w", err)
	}
	buf := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(buf, data)
	if !bytes.HasPrefix(buf, []byte("MEGA{")) {
		return a, errors.New("mega: attributes: wrong key")
	}
	js := bytes.TrimRight(buf[4:], "\x00")
	if err := json.Unmarshal(js, &a); err != nil {
		// Some clients leave garbage after the JSON object: keep the
		// outermost {...} and retry.
		if i := bytes.LastIndexByte(js, '}'); i > 0 {
			if err2 := json.Unmarshal(js[:i+1], &a); err2 == nil {
				return a, nil
			}
		}
		return a, fmt.Errorf("mega: attributes: %w", err)
	}
	return a, nil
}

// ecbDecrypt decrypts src (multiple of 16 bytes) with AES-ECB.
func ecbDecrypt(key, src []byte) ([]byte, error) {
	if len(src) == 0 || len(src)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: encrypted key has %d bytes", errBadKey, len(src))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(src))
	for i := 0; i < len(src); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], src[i:i+aes.BlockSize])
	}
	return out, nil
}

// ecbEncrypt is the inverse of ecbDecrypt (used by tests and hashing).
func ecbEncrypt(key, src []byte) ([]byte, error) {
	if len(src)%aes.BlockSize != 0 {
		return nil, errBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(src))
	for i := 0; i < len(src); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], src[i:i+aes.BlockSize])
	}
	return out, nil
}

// newCTRAt returns the AES-128-CTR keystream of k positioned at the absolute
// plaintext offset: counter = nonce || uint64(offset/16), then offset%16
// bytes are discarded.
func newCTRAt(k fileKey, offset int64) (cipher.Stream, error) {
	if offset < 0 {
		return nil, fmt.Errorf("mega: negative offset %d", offset)
	}
	block, err := aes.NewCipher(k.AES[:])
	if err != nil {
		return nil, err
	}
	var iv [16]byte
	copy(iv[:8], k.Nonce[:])
	binary.BigEndian.PutUint64(iv[8:], uint64(offset/aes.BlockSize))
	s := cipher.NewCTR(block, iv[:])
	if skip := int(offset % aes.BlockSize); skip > 0 {
		var junk [aes.BlockSize]byte
		s.XORKeyStream(junk[:skip], junk[:skip])
	}
	return s, nil
}

// chunkSize returns the size of MAC chunk index (0-based): 128 KiB * i for the first 8
// chunks (i = 1..8), then 1 MiB.
func chunkSize(index int) int64 {
	if index < 8 {
		return int64(index+1) * 128 * 1024
	}
	return 1024 * 1024
}

// macState computes MEGA's condensed MAC over a plaintext stream.
type macState struct {
	block    cipher.Block
	iv       [16]byte // nonce || nonce
	meta     [16]byte // running CBC over chunk MACs (zero IV)
	chunk    [16]byte // running CBC-MAC of the current chunk
	pending  []byte   // bytes not yet forming a full block
	chunkIdx int
	inChunk  int64 // bytes consumed in the current chunk
	total    int64
}

func newMACState(k fileKey) (*macState, error) {
	block, err := aes.NewCipher(k.AES[:])
	if err != nil {
		return nil, err
	}
	m := &macState{block: block}
	copy(m.iv[:8], k.Nonce[:])
	copy(m.iv[8:], k.Nonce[:])
	m.chunk = m.iv
	return m, nil
}

func (m *macState) absorbBlock(b []byte) {
	for i := 0; i < aes.BlockSize; i++ {
		m.chunk[i] ^= b[i]
	}
	m.block.Encrypt(m.chunk[:], m.chunk[:])
}

func (m *macState) closeChunk() {
	if len(m.pending) > 0 {
		var blk [16]byte
		copy(blk[:], m.pending)
		m.absorbBlock(blk[:])
		m.pending = m.pending[:0]
	}
	for i := 0; i < aes.BlockSize; i++ {
		m.meta[i] ^= m.chunk[i]
	}
	m.block.Encrypt(m.meta[:], m.meta[:])
	m.chunk = m.iv
	m.chunkIdx++
	m.inChunk = 0
}

// Write implements io.Writer.
func (m *macState) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := chunkSize(m.chunkIdx) - m.inChunk
		take := int64(len(p))
		if take > room {
			take = room
		}
		part := p[:take]
		p = p[take:]
		m.inChunk += take
		m.total += take
		if len(m.pending) > 0 {
			need := aes.BlockSize - len(m.pending)
			if need > len(part) {
				need = len(part)
			}
			m.pending = append(m.pending, part[:need]...)
			part = part[need:]
			if len(m.pending) == aes.BlockSize {
				m.absorbBlock(m.pending)
				m.pending = m.pending[:0]
			}
		}
		for len(part) >= aes.BlockSize {
			m.absorbBlock(part[:aes.BlockSize])
			part = part[aes.BlockSize:]
		}
		m.pending = append(m.pending, part...)
		if m.inChunk == chunkSize(m.chunkIdx) {
			m.closeChunk()
		}
	}
	return n, nil
}

// sum returns the condensed 8-byte MAC.
func (m *macState) sum() [8]byte {
	if m.inChunk > 0 {
		m.closeChunk()
	}
	var out [8]byte
	w := func(i int) uint32 { return binary.BigEndian.Uint32(m.meta[i*4:]) }
	binary.BigEndian.PutUint32(out[0:], w(0)^w(1))
	binary.BigEndian.PutUint32(out[4:], w(2)^w(3))
	return out
}

// VerifyMAC reads the decrypted file content from r and checks it against
// the meta-MAC embedded in key. key is the value of a hosters.Checksum with
// Algo hosters.AlgoMEGAMAC: the base64url 256-bit node key. It returns
// ErrMACMismatch when the content differs. Empty files are accepted (MEGA
// stores no meaningful MAC for them).
//
// The postprocess package can register it for AlgoMEGAMAC:
//
//	func(r io.Reader, value string) error { return mega.VerifyMAC(r, value) }
func VerifyMAC(r io.Reader, key string) error {
	k, err := parseFileKey(key)
	if err != nil {
		return err
	}
	m, err := newMACState(k)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(m, r, make([]byte, 256*1024)); err != nil {
		return fmt.Errorf("mega: reading content for MAC: %w", err)
	}
	if m.total == 0 {
		return nil
	}
	if got := m.sum(); got != k.MetaMAC {
		return ErrMACMismatch
	}
	return nil
}

// --- Login key derivation -------------------------------------------------

// prepareKeyV1 is MEGA's legacy password key derivation (65536 rounds of
// AES-ECB over a fixed seed with keys taken from the password).
func prepareKeyV1(password string) ([]byte, error) {
	pw := padZero([]byte(password), 4)
	var ciphers []cipher.Block
	for j := 0; j < len(pw); j += 16 {
		key := make([]byte, 16)
		copy(key, pw[j:min(j+16, len(pw))])
		c, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		ciphers = append(ciphers, c)
	}
	pkey := []byte{0x93, 0xC4, 0x67, 0xE3, 0x7D, 0xB0, 0xC7, 0xA4, 0xD1, 0xBE, 0x3F, 0x81, 0x01, 0x52, 0xCB, 0x56}
	for r := 0; r < 0x10000; r++ {
		for _, c := range ciphers {
			c.Encrypt(pkey, pkey)
		}
	}
	return pkey, nil
}

// stringHash is MEGA's v1 user handle: the e-mail folded into 128 bits and
// encrypted 16384 times with the password key; words 0 and 2 are kept.
func stringHash(s string, key []byte) (string, error) {
	b := padZero([]byte(s), 4)
	var h [16]byte
	for i := 0; i+4 <= len(b); i += 4 {
		j := (i / 4) & 3
		v := binary.BigEndian.Uint32(h[j*4:]) ^ binary.BigEndian.Uint32(b[i:])
		binary.BigEndian.PutUint32(h[j*4:], v)
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	for i := 0; i < 16384; i++ {
		c.Encrypt(h[:], h[:])
	}
	out := make([]byte, 8)
	copy(out[:4], h[0:4])
	copy(out[4:], h[8:12])
	return b64Encode(out), nil
}

// deriveV2 returns (password key, user handle) for v2 accounts:
// PBKDF2-HMAC-SHA512(password, salt, 100000, 32 bytes).
func deriveV2(password string, salt []byte) ([]byte, string) {
	dk := pbkdf2.Key([]byte(password), salt, 100000, 32, sha512.New)
	return dk[:16], b64Encode(dk[16:])
}

func padZero(b []byte, q int) []byte {
	if r := len(b) % q; r != 0 {
		b = append(b, make([]byte, q-r)...)
	}
	return b
}

// readMPI reads a MEGA multi-precision integer (2-byte bit length + bytes).
func readMPI(b []byte) (*big.Int, []byte, error) {
	if len(b) < 2 {
		return nil, nil, errors.New("mega: truncated MPI")
	}
	n := (int(b[0])<<8 | int(b[1]) + 7) / 8
	if len(b) < 2+n {
		return nil, nil, errors.New("mega: truncated MPI")
	}
	return new(big.Int).SetBytes(b[2 : 2+n]), b[2+n:], nil
}

// decryptSID recovers the session id from a login response: privk (RSA
// private key encrypted with the master key) decrypts csid; the sid is the
// first 43 bytes of the plaintext.
func decryptSID(masterKey []byte, privkB64, csidB64 string) (string, error) {
	enc, err := b64Decode(privkB64)
	if err != nil {
		return "", fmt.Errorf("mega: privk: %w", err)
	}
	if len(enc)%aes.BlockSize != 0 {
		enc = enc[:len(enc)-len(enc)%aes.BlockSize]
	}
	pk, err := ecbDecrypt(masterKey, enc)
	if err != nil {
		return "", fmt.Errorf("mega: privk: %w", err)
	}
	p, rest, err := readMPI(pk)
	if err != nil {
		return "", err
	}
	q, rest, err := readMPI(rest)
	if err != nil {
		return "", err
	}
	d, _, err := readMPI(rest)
	if err != nil {
		return "", err
	}
	csid, err := b64Decode(csidB64)
	if err != nil {
		return "", fmt.Errorf("mega: csid: %w", err)
	}
	c, _, err := readMPI(csid)
	if err != nil {
		return "", err
	}
	n := new(big.Int).Mul(p, q)
	if n.Sign() == 0 {
		return "", errors.New("mega: invalid RSA key")
	}
	r := new(big.Int).Exp(c, d, n).Bytes()
	if len(r) < 43 {
		return "", errors.New("mega: decrypted session id too short (wrong password?)")
	}
	return b64Encode(r[:43]), nil
}
