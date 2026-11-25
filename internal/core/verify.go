package core

import (
	"context"
	"crypto/md5"  //nolint:gosec // G501: hoster checksums (Drive publishes md5)
	"crypto/sha1" //nolint:gosec // G505: hoster checksums
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"strings"

	"admindescargas/internal/hosters"
)

func checksumSupported(algo string) bool {
	switch strings.ToLower(algo) {
	case hosters.AlgoSHA256, hosters.AlgoSHA1, hosters.AlgoMD5, hosters.AlgoCRC32:
		return true
	}
	return false
}

// verifyChecksum hashes path and compares it with ck (hex, any case).
func verifyChecksum(ctx context.Context, path string, ck *hosters.Checksum) (bool, error) {
	var h hash.Hash
	switch strings.ToLower(ck.Algo) {
	case hosters.AlgoSHA256:
		h = sha256.New()
	case hosters.AlgoSHA1:
		h = sha1.New() //nolint:gosec // G401: verifies the hoster's published checksum, not used for security
	case hosters.AlgoMD5:
		h = md5.New() //nolint:gosec // G401: verifies the hoster's published checksum, not used for security
	case hosters.AlgoCRC32:
		h = crc32.NewIEEE()
	default:
		return false, fmt.Errorf("unsupported checksum %q", ck.Algo)
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, rerr := f.Read(buf)
		h.Write(buf[:n])
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return false, rerr
		}
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), strings.TrimSpace(ck.Value)), nil
}
