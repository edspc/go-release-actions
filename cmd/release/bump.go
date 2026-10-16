package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// bump raises a part of the version in a file, replaces the old version in other files, and
// returns the new version. It writes nothing unless every file reads and the version parses.
func bump(root *os.Root, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet("bump", flag.ContinueOnError)
	part := fs.String("part", "", "major, minor or patch")
	file := fs.String("file", "", "file that holds the version")
	pattern := fs.String("pattern", defaultPattern, "regular expression, matched per line, with one group around the version")
	replace := fs.String("replace", "", "comma- or newline-separated files in which the old version is replaced wherever it stands alone")
	if err := parse(fs, args, stderr); err != nil {
		return "", err
	}
	if *file == "" {
		return "", errors.New("bump: -file is required")
	}
	content, start, end, err := findVersion(root, *file, *pattern)
	if err != nil {
		return "", err
	}
	old := string(content[start:end])
	version, err := nextVersion(old, *part)
	if err != nil {
		return "", err
	}
	paths := []string{*file}
	contents := []string{string(content[:start]) + version + string(content[end:])}
	for _, path := range list(*replace) {
		if err = local("replace", path); err != nil {
			return "", err
		}
		data, err := root.ReadFile(path)
		if err != nil {
			return "", err
		}
		paths = append(paths, path)
		contents = append(contents, replaceVersion(string(data), old, version))
	}
	for i, path := range paths {
		if err = root.WriteFile(path, []byte(contents[i]), 0o644); err != nil {
			return "", err
		}
	}
	return version, nil
}

// replaceVersion replaces old with version wherever it stands alone: 1.2.3 is not a part of
// 11.2.3, 1.2.30 or 1.2.3.4, while v1.2.3 and 1.2.3-rc.1 hold it.
func replaceVersion(text, old, version string) string {
	digit := func(i int) bool { return i >= 0 && i < len(text) && text[i] >= '0' && text[i] <= '9' }
	var b strings.Builder
	for from := 0; ; {
		i := strings.Index(text[from:], old)
		if i < 0 {
			b.WriteString(text[from:])
			return b.String()
		}
		i += from
		end := i + len(old)
		b.WriteString(text[from:i])
		if digit(i-1) || i > 0 && text[i-1] == '.' || digit(end) || end < len(text) && text[end] == '.' && digit(end+1) {
			b.WriteString(old)
		} else {
			b.WriteString(version)
		}
		from = end
	}
}

// nextVersion raises part of a MAJOR.MINOR.PATCH version and resets the parts after it.
func nextVersion(version, part string) (string, error) {
	var i int
	switch part {
	case "major":
		i = 0
	case "minor":
		i = 1
	case "patch":
		i = 2
	default:
		return "", fmt.Errorf("part must be major, minor or patch, got %q", part)
	}
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return "", fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", version)
	}
	numbers := make([]int, len(fields))
	for j, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return "", fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", version)
		}
		numbers[j] = n
	}
	numbers[i]++
	for j := i + 1; j < len(numbers); j++ {
		numbers[j] = 0
	}
	return fmt.Sprintf("%d.%d.%d", numbers[0], numbers[1], numbers[2]), nil
}
