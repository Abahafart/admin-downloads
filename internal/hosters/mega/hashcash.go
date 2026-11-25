package mega

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MEGA answers some API requests (notably logins from new IPs) with HTTP 402
// and an "X-Hashcash: 1:<easiness>:<timestamp>:<token>" proof-of-work
// challenge. The request must be repeated with "X-Hashcash: 1:<token>:<cash>"
// where cash is a 4-byte prefix such that SHA-256(prefix || token * 262144)
// starts with a 32-bit value <= threshold(easiness).

const (
	hashcashReplications = 262144
	hashcashSlot         = 48
)

func parseHashcash(h string) (easiness int, token string, err error) {
	parts := strings.Split(strings.TrimSpace(h), ":")
	if len(parts) != 4 || parts[0] != "1" {
		return 0, "", errors.New("unsupported challenge")
	}
	e, err := strconv.Atoi(parts[1])
	if err != nil || e < 0 || e > 255 {
		return 0, "", errors.New("bad easiness")
	}
	if !isB64(parts[3]) {
		return 0, "", errors.New("bad token")
	}
	return e, parts[3], nil
}

func hashcashThreshold(easiness int) uint32 {
	return uint32((((easiness & 63) << 1) + 1) << ((easiness>>6)*7 + 3)) //nolint:gosec // G115: easiness is 0..255, so the result is at most 127<<24
}

// solveHashcashHeader solves the challenge in header h and returns the value
// for the X-Hashcash request header.
func solveHashcashHeader(ctx context.Context, h string, timeout time.Duration) (string, error) {
	easiness, token, err := parseHashcash(h)
	if err != nil {
		return "", err
	}
	tok, err := b64Decode(token)
	if err != nil {
		return "", fmt.Errorf("bad token: %w", err)
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workers := min(max(1, runtime.NumCPU()/2), 255)
	res := make(chan string, workers)
	for w := 0; w < workers; w++ {
		go func(start byte) { res <- hashcashWork(ctx, tok, hashcashThreshold(easiness), start, byte(workers)) }(byte(w)) //nolint:gosec // G115: workers is capped at 255 above
	}
	for w := 0; w < workers; w++ {
		select {
		case r := <-res:
			if r != "" {
				cancel()
				return "1:" + token + ":" + r, nil
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", errors.New("no solution found")
}

// hashcashWork searches prefixes whose first byte starts at start and grows
// by step (workers split the search space).
func hashcashWork(ctx context.Context, token []byte, threshold uint32, start, step byte) string {
	tok := padZero(append([]byte(nil), token...), 16)
	buf := make([]byte, 4+hashcashReplications*hashcashSlot)
	for i := 0; i < hashcashReplications; i++ {
		copy(buf[4+i*hashcashSlot:], tok)
	}
	prefix := uint32(start)
	for iter := 0; ; iter++ {
		if iter%64 == 0 && ctx.Err() != nil {
			return ""
		}
		binary.LittleEndian.PutUint32(buf[:4], prefix)
		sum := sha256.Sum256(buf)
		if binary.BigEndian.Uint32(sum[:4]) <= threshold {
			return base64.RawURLEncoding.EncodeToString(buf[:4])
		}
		next := prefix + uint32(max(step, 1))
		if next < prefix {
			return ""
		}
		prefix = next
	}
}
