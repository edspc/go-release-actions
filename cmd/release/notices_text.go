package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// File identity also catches a license reached through a symlink or a differently
// cased path on a case-insensitive filesystem, before the notices tree is removed.
func checkLicenseOutsideOutput(root *os.Root, dir, license string) error {
	info, err := root.Stat(license)
	if err != nil {
		return err
	}
	out, err := root.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	return fs.WalkDir(out.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		file, err := entry.Info()
		if err != nil {
			return err
		}
		if os.SameFile(info, file) {
			return errors.New("-license must be outside -out")
		}
		return nil
	})
}

// licenseNotices reads all notices before writing their combined text, so a missing
// or unreadable license never leaves a partial release footer.
func licenseNotices(root *os.Root, dir, license string) (map[string][]byte, error) {
	data, err := root.ReadFile(license)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("empty license notice %s", license)
	}
	files := map[string][]byte{license: data}
	err = fs.WalkDir(root.FS(), filepath.ToSlash(filepath.Clean(dir)), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("notice %s is not a regular file", path)
		}
		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return fmt.Errorf("empty license notice %s", path)
		}
		files[path] = data
		return nil
	})
	return files, err
}

func combineNotices(root *os.Root, dir, license, out string) error {
	files, err := licenseNotices(root, dir, license)
	if err != nil {
		return err
	}
	output, err := root.Stat(out)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var text bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if output != nil {
			info, err := root.Stat(name)
			if err != nil {
				return err
			}
			if os.SameFile(output, info) {
				return fmt.Errorf("combined output aliases license notice %s", name)
			}
		}
		text.Write(files[name])
		text.WriteString("\n\n")
	}
	if err = root.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return root.WriteFile(out, text.Bytes(), 0o644)
}
