package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SidecarSuffix is appended to the .part path for the segment-map sidecar
// ("<file>.part.adesc").
const SidecarSuffix = ".adesc"

const sidecarVersion = 1

// SidecarInfo identifies the download in the sidecar so it can be matched
// again without the database. It must not contain secrets or signed URLs.
type SidecarInfo struct {
	Hoster string `json:"hoster"`
	FileID string `json:"file_id,omitempty"`
	Name   string `json:"name"`
}

type sidecarFile struct {
	Version   int         `json:"version"`
	Info      SidecarInfo `json:"info"`
	Progress  Progress    `json:"progress"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// SidecarPath returns the sidecar path for a .part path.
func SidecarPath(partPath string) string { return partPath + SidecarSuffix }

// ReadSidecar loads the sidecar of partPath. It returns an error wrapping
// os.ErrNotExist when there is none.
func ReadSidecar(partPath string) (SidecarInfo, Progress, error) {
	data, err := os.ReadFile(SidecarPath(partPath))
	if err != nil {
		return SidecarInfo{}, Progress{}, err
	}
	var sf sidecarFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return SidecarInfo{}, Progress{}, fmt.Errorf("downloader: parse sidecar: %w", err)
	}
	if sf.Version != sidecarVersion {
		return SidecarInfo{}, Progress{}, fmt.Errorf("downloader: sidecar version %d unsupported", sf.Version)
	}
	return sf.Info, sf.Progress, nil
}

// WriteSidecar atomically writes the sidecar (temp file + fsync + rename).
func WriteSidecar(partPath string, info SidecarInfo, p Progress) error {
	data, err := json.Marshal(sidecarFile{Version: sidecarVersion, Info: info, Progress: p, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("downloader: marshal sidecar: %w", err)
	}
	dst := SidecarPath(partPath)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".adesc-*.tmp")
	if err != nil {
		return fmt.Errorf("downloader: sidecar temp: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("downloader: write sidecar: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("downloader: sync sidecar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("downloader: close sidecar: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("downloader: rename sidecar: %w", err)
	}
	return nil
}

// RemoveFiles deletes the .part file and its sidecar; missing files are not
// an error.
func RemoveFiles(partPath string) error {
	var errs []error
	for _, p := range []string{partPath, SidecarPath(partPath)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
