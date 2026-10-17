package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// GoReleaser uses the same type for build outputs and uploadable binaries; the
// archive format distinguishes the entries whose names become release assets.
type releaseArtifact struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Goos  string `json:"goos"`
	Extra struct {
		Format string `json:"Format"`
	} `json:"extra"`
}

func checkBinaries(root *os.Root, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("check-binaries", flag.ContinueOnError)
	manifest := flags.String("artifacts", "dist/artifacts.json", "GoReleaser artifacts JSON file")
	names := flags.String("names", "", "comma- or newline-separated expected release asset names")
	noticeDir := flags.String("notices", "", "directory holding the gathered Go and module notices")
	combined := flags.String("combined", "", "file containing the project license and all gathered notices")
	license := flags.String("license", "LICENSE", "project license file")
	required := flags.String("required-notices", "", "comma- or newline-separated glob patterns, relative to the notices directory, each matching at least one notice file")
	if err := parse(flags, args, stderr); err != nil {
		return err
	}
	for name, path := range map[string]string{"artifacts": *manifest, "notices": *noticeDir, "combined": *combined, "license": *license} {
		if err := local(name, path); err != nil {
			return err
		}
	}
	expected := map[string]bool{}
	for _, name := range list(*names) {
		if _, ok := expected[name]; ok {
			return fmt.Errorf("duplicate expected asset %q", name)
		}
		expected[name] = false
	}
	if len(expected) == 0 {
		return errors.New("check-binaries: -names is required")
	}
	data, err := root.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var artifacts []releaseArtifact
	if err = json.Unmarshal(data, &artifacts); err != nil {
		return fmt.Errorf("artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if artifact.Type != "Metadata" && artifact.Type != "Binary" {
			return fmt.Errorf("unexpected release artifact %q of type %q; only binaries are allowed", artifact.Name, artifact.Type)
		}
		if artifact.Extra.Format != "binary" {
			continue
		}
		seen, ok := expected[artifact.Name]
		if !ok || seen || artifact.Type != "Binary" {
			return fmt.Errorf("unexpected or duplicate binary asset %q", artifact.Name)
		}
		if err = checkBinary(root, artifact); err != nil {
			return fmt.Errorf("asset %s: %w", artifact.Name, err)
		}
		expected[artifact.Name] = true
	}
	for name, seen := range expected {
		if !seen {
			return fmt.Errorf("missing binary asset %q", name)
		}
	}
	return checkLicenseNotices(root, *noticeDir, *combined, *license, list(*required))
}

func checkBinary(root *os.Root, artifact releaseArtifact) error {
	if err := local("artifact path", artifact.Path); err != nil {
		return err
	}
	file, err := root.Open(artifact.Path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("not a regular executable file")
	}
	var magic [4]byte
	if _, err = io.ReadFull(file, magic[:]); err != nil {
		return err
	}
	switch artifact.Goos {
	case "linux":
		if magic == [4]byte{0x7f, 'E', 'L', 'F'} {
			return nil
		}
	case "darwin":
		switch magic {
		case [4]byte{0xcf, 0xfa, 0xed, 0xfe}, // thin 64-bit Mach-O
			[4]byte{0xca, 0xfe, 0xba, 0xbe}, // universal Mach-O
			[4]byte{0xca, 0xfe, 0xba, 0xbf}: // universal Mach-O with 64-bit offsets
			return nil
		}
	default:
		return fmt.Errorf("unsupported binary OS %q; want linux or darwin", artifact.Goos)
	}
	return fmt.Errorf("invalid %s executable signature", artifact.Goos)
}

func checkLicenseNotices(root *os.Root, dir, combined, license string, required []string) error {
	notices, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = notices.Close() }()
	for _, pattern := range append([]string{"go/LICENSE"}, required...) {
		if err = local("required-notices", pattern); err != nil {
			return err
		}
		matches, err := fs.Glob(notices.FS(), filepath.ToSlash(filepath.Clean(pattern)))
		if err != nil {
			return fmt.Errorf("required notice %q: %w", pattern, err)
		}
		found := false
		for _, match := range matches {
			info, err := notices.Stat(match)
			if err != nil {
				return err
			}
			found = found || info.Mode().IsRegular()
		}
		if !found {
			return fmt.Errorf("missing required notice %q", pattern)
		}
	}
	text, err := root.ReadFile(combined)
	if err != nil {
		return err
	}
	files, err := licenseNotices(root, dir, license)
	if err != nil {
		return err
	}
	for name, file := range files {
		if !bytes.Contains(text, file) {
			return fmt.Errorf("combined notices are missing %s", name)
		}
	}
	return nil
}
