package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// notices gathers into a directory the license notices that distributions of a Go program
// ship: Go's own and those of every module linked into the program on any of the platforms.
// With a version, it first checks that a release has the version committed in a file.
func notices(root *os.Root, args []string, goCmd goTool, stderr io.Writer) error {
	flags := flag.NewFlagSet("notices", flag.ContinueOnError)
	outDir := flags.String("out", "", "relative path of the directory to gather the notices in; replaced on every run, so it must be empty or hold notices of an earlier run")
	pkg := flags.String("main", ".", "main package of the program")
	platforms := flags.String("platforms", "", "comma- or newline-separated GOOS/GOARCH pairs the program is built for; when empty, the one go builds for by default, from GOOS and GOARCH of the environment or else the host's")
	version := flags.String("version", "", "version being released, checked against -file")
	file := flags.String("file", "", "file that holds the committed version")
	pattern := flags.String("pattern", defaultPattern, "regular expression, matched per line, with one group around the version")
	combined := flags.String("combined", "", "optional file for the project license and gathered notices, for release notes")
	license := flags.String("license", "LICENSE", "project license file outside -out, included with -combined")
	snapshot := flags.Bool("snapshot", false, "a snapshot build, whose version is not checked")
	if err := parse(flags, args, stderr); err != nil {
		return err
	}
	if *combined != "" {
		for name, path := range map[string]string{"combined": *combined, "license": *license} {
			if err := local(name, path); err != nil {
				return err
			}
		}
		dir := filepath.Clean(*outDir)
		out := filepath.Clean(*combined)
		// The notices directory is replaced before the project license is combined.
		projectLicense := filepath.Clean(*license)
		if projectLicense == dir || strings.HasPrefix(projectLicense, dir+string(filepath.Separator)) {
			return errors.New("-license must be outside -out")
		}
		if out == filepath.Clean(*license) || out == dir || strings.HasPrefix(out, dir+string(filepath.Separator)) {
			return errors.New("-combined must differ from -license and be outside -out")
		}
		if _, err := root.ReadFile(*license); err != nil {
			return err
		}
	}
	if *version != "" && !*snapshot {
		if *file == "" {
			return errors.New("notices: -file is required to check -version")
		}
		content, start, end, err := findVersion(root, *file, *pattern)
		if err != nil {
			return err
		}
		if string(content[start:end]) != *version {
			return fmt.Errorf("version %s does not match %s committed in %s", *version, content[start:end], *file)
		}
	}
	dir, err := outputDir(root, *outDir)
	if err != nil {
		return err
	}
	if *combined != "" {
		if err = checkLicenseOutsideOutput(root, dir, *license); err != nil {
			return err
		}
	}
	targets := [][]string{nil} // go's default, from GOOS and GOARCH of the environment
	if pairs := list(*platforms); len(pairs) > 0 {
		targets = targets[:0]
		for _, pair := range pairs {
			goos, goarch, ok := strings.Cut(pair, "/")
			if !ok || goos == "" || goarch == "" {
				return fmt.Errorf("platform %q is not GOOS/GOARCH", pair)
			}
			targets = append(targets, []string{"GOOS=" + goos, "GOARCH=" + goarch})
		}
	}
	modules := map[string]string{} // directory by module@version
	for _, target := range targets {
		// Release builds of Go programs usually disable cgo; its packages can add modules.
		out, err := goCmd(append([]string{"CGO_ENABLED=0"}, target...),
			"list", "-mod=readonly", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}", *pkg)
		if err != nil {
			return fmt.Errorf("listing modules %v: %w", target, err)
		}
		for line := range strings.Lines(string(out)) {
			name, moduleDir, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
			if name == "" {
				continue
			}
			if moduleDir == "" {
				return fmt.Errorf("no module directory found for %s", name)
			}
			modules[name] = moduleDir
		}
	}
	if err = root.RemoveAll(dir); err != nil {
		return err
	}
	goroot, err := goCmd(nil, "env", "GOROOT")
	if err != nil {
		return fmt.Errorf("go env GOROOT: %w", err)
	}
	if err = copyFile(root, filepath.Join(strings.TrimSpace(string(goroot)), "LICENSE"), filepath.Join(dir, "go", "LICENSE")); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(modules)) {
		if err = copyNotices(root, modules[name], filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if *combined != "" {
		return combineNotices(root, dir, *license, *combined)
	}
	return nil
}

// outputDir returns the directory to gather the notices in. It is removed and created again,
// so it must be strictly inside the current one and either missing, empty, or the output of
// an earlier run, which always holds go/LICENSE: a mistyped -out must not delete the sources.
func outputDir(root *os.Root, out string) (string, error) {
	if out == "" {
		return "", errors.New("notices: -out is required")
	}
	dir := filepath.Clean(out)
	if !filepath.IsLocal(dir) || dir == "." {
		return "", fmt.Errorf("-out %q must be a relative path inside the current directory", out)
	}
	f, err := root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return dir, nil
	}
	if err != nil {
		return "", err
	}
	names, err := f.Readdirnames(1)
	_ = f.Close()
	if errors.Is(err, io.EOF) {
		return dir, nil
	}
	if err != nil {
		return "", fmt.Errorf("-out %q: %w", out, err)
	}
	if _, err = root.Stat(filepath.Join(dir, "go", "LICENSE")); err != nil {
		return "", fmt.Errorf("-out %q holds files, such as %s, that no earlier run gathered; refusing to replace it", out, names[0])
	}
	return dir, nil
}

// copyNotices copies the notice files at the top of moduleDir into dir: those a module's
// license obliges us to ship along with its code.
func copyNotices(root *os.Root, moduleDir, dir string) error {
	prefixes := []string{"LICENSE", "COPYING", "NOTICE", "AUTHORS", "PATENTS"}
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(entry.Name(), p) }) {
			continue
		}
		if err = copyFile(root, filepath.Join(moduleDir, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("no license notices found")
	}
	return nil
}

// copyFile copies src, a file of Go or of the module cache, to dst in root.
func copyFile(root *os.Root, src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err = root.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return root.WriteFile(dst, data, 0o644)
}
