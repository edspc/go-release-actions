// Command release prepares releases of Go projects. Run it from the project's root:
//
//	release bump -part major|minor|patch -file FILE [-pattern REGEXP] [-replace FILES]
//	release notices -out DIR [-main PACKAGE] [-platforms LIST] [-version VERSION -file FILE [-pattern REGEXP]] [-snapshot=BOOL] [-combined FILE [-license LICENSE]]
//	release check-binaries -names NAMES -notices DIR -combined FILE [-artifacts dist/artifacts.json] [-license LICENSE] [-required-notices PATTERNS]
//
// -h after a subcommand lists its flags. Files of the project, given by flags, are relative
// paths inside the working directory: they are read and written through an os.Root there, so
// neither a path nor a symbolic link leads out of it.
//
// bump prints only the new version to stdout, for a workflow to take as is; errors are
// logged to stderr.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const usage = "usage: release bump|notices|check-binaries [flags]; -h after a subcommand lists its flags"

// defaultPattern finds a version declared as `const version = "X.Y.Z"`.
const defaultPattern = `^const version = "([^"]*)"$`

// goTool runs the go command in the current directory with env added to the environment and
// returns its stdout.
type goTool func(env []string, args ...string) ([]byte, error)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	out, err := run(os.Args[1:], runGo, os.Stderr)
	if err != nil {
		log.Error("release failed", "error", err)
		os.Exit(1)
	}
	if out != "" {
		_, _ = fmt.Fprintln(os.Stdout, out)
	}
}

// run runs a subcommand; help goes to stderr, never to stdout, which carries the result.
func run(args []string, goCmd goTool, stderr io.Writer) (string, error) {
	if len(args) == 0 || args[0] != "bump" && args[0] != "notices" && args[0] != "check-binaries" {
		return "", errors.New(usage)
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if args[0] == "bump" {
		return bump(root, args[1:], stderr)
	}
	if args[0] == "check-binaries" {
		return "", checkBinaries(root, args[1:], stderr)
	}
	return "", notices(root, args[1:], goCmd, stderr)
}

func runGo(env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// parse parses the flags of a subcommand, which takes no other arguments, and lists them on
// stderr when asked to.
func parse(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stderr)
			_, _ = fmt.Fprintf(stderr, "Flags of release %s:\n", fs.Name())
			fs.PrintDefaults()
		}
		return fmt.Errorf("%s: %w", fs.Name(), err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s: unexpected arguments %q", fs.Name(), fs.Args())
	}
	return nil
}

// local refuses a path that is not relative and inside the working directory with a clearer
// message than the os.Root error that would follow.
func local(flagName, path string) error {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("-%s %q must be a relative path inside the current directory", flagName, path)
	}
	return nil
}

// list splits a comma- or newline-separated list, as action inputs hold it, without the
// spaces around its items.
func list(s string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// findVersion returns the content of file and the position of the one version in it: the
// group of pattern, matched per line, holding a MAJOR.MINOR.PATCH version.
func findVersion(root *os.Root, file, pattern string) (content []byte, start, end int, err error) {
	if err = local("file", file); err != nil {
		return nil, 0, 0, err
	}
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("pattern: %w", err)
	}
	if re.NumSubexp() != 1 {
		return nil, 0, 0, errors.New("pattern must have exactly one group, around the version")
	}
	content, err = root.ReadFile(file)
	if err != nil {
		return nil, 0, 0, err
	}
	matches := re.FindAllSubmatchIndex(content, -1)
	if len(matches) != 1 || matches[0][2] < 0 {
		return nil, 0, 0, fmt.Errorf("found %d versions in %s, want one", len(matches), file)
	}
	start, end = matches[0][2], matches[0][3]
	// Leading zeroes are refused, as in Go module and tag versions.
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	if !semver.Match(content[start:end]) {
		return nil, 0, 0, fmt.Errorf("version %q in %s is not MAJOR.MINOR.PATCH", content[start:end], file)
	}
	return content, start, end, nil
}
