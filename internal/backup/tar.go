package backup

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// tarDir writes every regular file under srcDir into a tar at outPath, with slash-relative names (so it
// restores identically on any OS). Sub-directories are implied by the file names; empty dirs are dropped.
// Returns the number of files archived (0 → an absent/empty source, which callers treat as "nothing to do").
func tarDir(srcDir, outPath string) (int, error) {
	out, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create tar %s: %w", outPath, err)
	}
	defer func() { _ = out.Close() }()
	tw := tar.NewWriter(out)

	n := 0
	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		n++
		return nil
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			_ = tw.Close()
			return 0, nil // absent source → empty archive, not an error
		}
		_ = tw.Close()
		return 0, fmt.Errorf("tar %s: %w", srcDir, walkErr)
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	return n, nil
}

// untar extracts a tar (from tarDir) into destDir, creating parent dirs and guarding against path-escape
// (zip-slip): a member whose cleaned path would land outside destDir is rejected.
func untar(srcPath, destDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open tar %s: %w", srcPath, err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar %s: %w", srcPath, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		target := filepath.Join(destAbs, filepath.FromSlash(hdr.Name))
		if target != destAbs && !strings.HasPrefix(target, destAbs+string(os.PathSeparator)) {
			return fmt.Errorf("tar member escapes destination: %s", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		w, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil { //nolint:gosec // sizes are our own backups
			_ = w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
}
