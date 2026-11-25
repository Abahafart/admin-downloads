package downloader

import (
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"admindescargas/internal/hosters"
)

// ErrRemoteChanged means the remote file is not the one the saved progress
// belongs to (ETag / size / Last-Modified changed, If-Range answered with a
// full 200, or 416). The caller must discard the progress and restart from
// zero (and tell the user).
var ErrRemoteChanged = errors.New("downloader: remote file changed")

// ErrIncomplete is returned when a run ended without error but the segment
// map is not complete (should not happen; defensive).
var ErrIncomplete = errors.New("downloader: download incomplete")

// Segment is a byte range [Start, End) of the file; End = -1 for an
// open-ended single stream of unknown size. Done counts the bytes from Start
// already written.
type Segment struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"`
}

// Complete reports whether the segment has every byte.
func (s Segment) Complete() bool { return s.End >= 0 && s.Start+s.Done >= s.End }

// Remote identifies the remote file a progress belongs to.
type Remote struct {
	// Size in bytes, -1 if unknown.
	Size         int64             `json:"size"`
	ETag         string            `json:"etag,omitempty"`
	LastModified string            `json:"last_modified,omitempty"`
	Checksum     *hosters.Checksum `json:"checksum,omitempty"`
}

// Progress is the resumable state of a download.
type Progress struct {
	Remote Remote `json:"remote"`
	// Resumable is true when the server honors Range requests.
	Resumable bool      `json:"resumable"`
	Segments  []Segment `json:"segments"`
}

// Done returns the number of bytes covered by the segments.
func (p Progress) Done() int64 {
	var n int64
	for _, s := range p.Segments {
		n += s.Done
	}
	return n
}

// Complete reports whether every segment is complete (and there is at least
// one).
func (p Progress) Complete() bool {
	if len(p.Segments) == 0 {
		return false
	}
	for _, s := range p.Segments {
		if !s.Complete() {
			return false
		}
	}
	return true
}

// Clone returns a deep copy.
func (p Progress) Clone() Progress {
	c := p
	c.Segments = slices.Clone(p.Segments)
	if p.Remote.Checksum != nil {
		ck := *p.Remote.Checksum
		c.Remote.Checksum = &ck
	}
	return c
}

func (p Progress) equal(o Progress) bool {
	return p.Resumable == o.Resumable && p.Remote.Size == o.Remote.Size && p.Remote.ETag == o.Remote.ETag &&
		p.Remote.LastModified == o.Remote.LastModified && slices.Equal(p.Segments, o.Segments)
}

// Backoff returns the wait before retry number attempt (1-based):
// exponential from base, capped at max, with jitter in [d/2, d).
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half) //nolint:gosec // G404: retry jitter, not security sensitive
}
