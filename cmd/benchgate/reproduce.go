package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// commandPath is the command's package, which go install builds.
const commandPath = "github.com/wzshiming/benchgate/cmd/benchgate"

// mainVersion is the main module's version in the build information, empty without it.
func mainVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

// reproduce tells how to install the gate at version, the main module's in the build information, and repeat this run.
func (o options) reproduce(version string) string {
	if version == "" || version == "(devel)" || strings.HasSuffix(version, "+dirty") {
		version = "latest" // not a version go install takes
	}
	return fmt.Sprintf("With worktrees `<base>` at %s and `<head>` at %s, install the gate with `go install %s@%s` and run `%s` with absolute paths and a new directory `<out>`.\n",
		o.baseLabel, o.headLabel, commandPath, version, o.command())
}

// command is a command line of the installed gate repeating this run, with placeholders for local paths.
func (o options) command() string {
	args := []string{"benchgate -base <base> -head <head>",
		"-base-label " + quote(o.baseLabel), "-head-label " + quote(o.headLabel), "-config " + o.configArg()}
	if o.groups != "" {
		args = append(args, "-groups "+quote(o.groups))
	}
	if o.measureIdentical != nil {
		args = append(args, "-measure-identical="+strconv.FormatBool(*o.measureIdentical))
	}
	return strings.Join(append(args, "-out <out>"), " ")
}

// configArg names the configuration file by its place in the head tree, or else by a placeholder.
func (o options) configArg() string {
	head, _ := filepath.Abs(o.head)
	config, _ := filepath.Abs(o.config)
	if rel, err := filepath.Rel(head, config); err == nil && filepath.IsLocal(rel) {
		return "<head>/" + filepath.ToSlash(rel)
	}
	return "<config>"
}

var plainWord = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// quote is s as one word of a POSIX shell command.
func quote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
