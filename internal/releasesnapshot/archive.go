package releasesnapshot

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxCompressedBytes = 64 << 20
	MaxExtractedBytes  = 256 << 20
	MaxFileBytes       = 32 << 20
	MaxArchiveEntries  = 20000
	MaxPathDepth       = 64
	MaxPathBytes       = 1024
	MaxSegmentBytes    = 255
)

var errTooLarge = errors.New("archive too large")

func downloadArchive(ctx context.Context, body io.ReadCloser, destination string) (string, error) {
	defer body.Close()
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(f, h), io.LimitReader(body, MaxCompressedBytes+1))
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(destination)
		return "", err
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return "", closeErr
	}
	if written > MaxCompressedBytes {
		_ = os.Remove(destination)
		return "", errTooLarge
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractArchive(ctx context.Context, archivePath, destination string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	buffered := bufio.NewReader(f)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return errors.New("invalid gzip")
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	var root string
	seen := map[string]struct{}{}
	caseFolded := map[string]struct{}{}
	var entries int
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid tar")
		}
		entries++
		if entries > MaxArchiveEntries {
			return errTooLarge
		}
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return errors.New("unsupported archive entry")
		}
		if h.Size < 0 || h.Size > MaxFileBytes || hasSparse(h) {
			return errors.New("unsafe archive entry")
		}
		name := h.Name
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		archiveName, err := validArchiveName(name)
		if err != nil {
			return err
		}
		parts := strings.Split(archiveName, "/")
		if root == "" {
			root = parts[0]
		} else if parts[0] != root {
			return errors.New("multiple archive roots")
		}
		if len(parts) == 1 {
			if h.Typeflag != tar.TypeDir {
				return errors.New("root is not directory")
			}
			continue
		}
		rel := strings.Join(parts[1:], "/")
		if _, ok := seen[rel]; ok {
			return errors.New("duplicate archive entry")
		}
		seen[rel] = struct{}{}
		fold := strings.ToLower(rel)
		if _, ok := caseFolded[fold]; ok {
			return errors.New("case-folding archive collision")
		}
		caseFolded[fold] = struct{}{}
		out := filepath.Join(destination, filepath.FromSlash(rel))
		if !within(destination, out) {
			return errors.New("workspace escape")
		}
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(out, 0o700); err != nil {
				return err
			}
			continue
		}
		total += h.Size
		if total > MaxExtractedBytes {
			return errTooLarge
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if h.FileInfo().Mode()&0o111 != 0 {
			mode = 0o700
		}
		outFile, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		written, copyErr := copyContext(ctx, outFile, io.LimitReader(tr, h.Size+1))
		closeErr := outFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != h.Size {
			return errors.New("truncated tar entry")
		}
	}
	if root == "" {
		return errors.New("rootless archive")
	}
	// gzip's EOF check validates checksum. A single immutable stream is
	// required so trailing members and raw bytes cannot be smuggled in.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return errors.New("invalid gzip")
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		return errors.New("trailing archive data")
	}
	return nil
}

func hasSparse(h *tar.Header) bool {
	for key := range h.PAXRecords {
		if strings.HasPrefix(strings.ToLower(key), "gnu.sparse") {
			return true
		}
	}
	return h.Format == tar.FormatGNU && (h.PAXRecords["GNU.sparse.map"] != "")
}
func validArchiveName(name string) (string, error) {
	if !utf8.ValidString(name) || name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || len(name) > MaxPathBytes || path.Clean(name) != name {
		return "", errors.New("unsafe archive path")
	}
	parts := strings.Split(name, "/")
	if len(parts) > MaxPathDepth {
		return "", errors.New("unsafe archive path")
	}
	for _, part := range parts {
		if !safeSegment(part) {
			return "", errors.New("unsafe archive path")
		}
	}
	return name, nil
}
func safeSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > MaxSegmentBytes || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") || strings.ContainsAny(value, "<>:\"|?*") {
		return false
	}
	base := strings.TrimSuffix(strings.ToUpper(value), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 32<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		read, err := r.Read(buf)
		if read > 0 {
			written, writeErr := w.Write(buf[:read])
			n += int64(written)
			if writeErr != nil {
				return n, writeErr
			}
			if written != read {
				return n, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}
