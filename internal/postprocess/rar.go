package postprocess

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/nwaples/rardecode/v2"
)

func (x *extraction) extractRar(pw string) error {
	var opts []rardecode.Option
	if pw != "" {
		opts = append(opts, rardecode.Password(pw))
	}
	encrypted := false
	classify := func(name string, err error) error {
		if ctxErr := x.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		switch {
		case isWriteError(err):
			return err
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%w: %v", errMissingVolume, err)
		case errors.Is(err, rardecode.ErrArchiveEncrypted),
			errors.Is(err, rardecode.ErrArchivedFileEncrypted),
			errors.Is(err, rardecode.ErrBadPassword):
			return fmt.Errorf("%w: %q: %v", errBadPassword, name, err)
		case (encrypted || pw != "") && (errors.Is(err, rardecode.ErrBadFileChecksum) ||
			errors.Is(err, rardecode.ErrBadHeaderCRC) || errors.Is(err, rardecode.ErrCorruptBlockHeader) ||
			errors.Is(err, rardecode.ErrCorruptFileHeader)):
			// RAR4 and RAR5 without a check value only notice a wrong
			// password through checksums.
			return fmt.Errorf("%w: %q: %v", errBadPassword, name, err)
		case x.set.multi && (errors.Is(err, rardecode.ErrMultiVolume) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, rardecode.ErrUnexpectedArcEnd)):
			return fmt.Errorf("%w: %v", errMissingVolume, err)
		}
		return fmt.Errorf("%w: %q: %v", ErrCorrupt, name, err)
	}

	// List first: sizes for progress and an early password check.
	list, err := rardecode.List(x.set.first, opts...)
	if err != nil {
		return classify("", err)
	}
	var total int64
	for _, f := range list {
		total += f.UnPackedSize
		if f.Encrypted || f.HeaderEncrypted {
			encrypted = true
		}
	}
	if encrypted && pw == "" {
		return fmt.Errorf("%w: archive is encrypted", errBadPassword)
	}
	x.pw.p.Total = total

	rc, err := rardecode.OpenReader(x.set.first, opts...)
	if err != nil {
		return classify("", err)
	}
	defer rc.Close()
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		h, err := rc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return classify("", err)
		}
		if h.Encrypted {
			encrypted = true
		}
		if h.LinkType != rardecode.LinkTypeNone {
			x.reject(h.Name, "link")
			continue
		}
		switch x.entryKind(h.Name, h.Mode(), h.IsDir) {
		case "dir":
			if err := x.writeDir(h.Name); err != nil {
				return err
			}
		case "file":
			if err := x.writeFile(h.Name, rc); err != nil {
				return classify(h.Name, err)
			}
		}
	}
	return nil
}
