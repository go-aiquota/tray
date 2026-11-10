// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Limits on what an archive may unpack to (an archive is downloaded data).
const (
	maxEntries   = 16
	maxFileBytes = 128 << 20
)

// verifySum checks archive against its line in the SHA256SUMS file.
func verifySum(archive, sums string) error {
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	list, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	got := hex.EncodeToString(h[:])
	name := filepath.Base(archive)
	for _, l := range strings.Split(string(list), "\n") {
		sum, n, ok := strings.Cut(l, "  ")
		if ok && n == name {
			if sum != got {
				return fmt.Errorf("%s: checksum %s, SHA256SUMS says %s", name, got, sum)
			}
			return nil
		}
	}
	return fmt.Errorf("%s: no line in SHA256SUMS", name)
}

// safeName is the entry's file name when it is a plain file directly inside
// the archive's one directory dir, else "".
func safeName(entry, dir string) string {
	if path.Clean(entry) != entry || path.Dir(entry) != dir {
		return ""
	}
	return path.Base(entry)
}

// extract unpacks a .tar.gz or .zip into dest and returns the directory that
// holds the files. It refuses links, directories other than the archive's
// own, names that leave it, more than maxEntries entries and any file over
// maxFileBytes.
func extract(archive, dest string) (string, error) {
	dir := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(archive), ".tar.gz"), ".zip")
	write := func(name string, r io.Reader) error {
		f, err := os.OpenFile(filepath.Join(dest, dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(r, maxFileBytes+1))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && n > maxFileBytes {
			err = errors.New("file over the size limit")
		}
		return err
	}
	if err := os.MkdirAll(filepath.Join(dest, dir), 0o755); err != nil {
		return "", err
	}
	count := 0
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		for _, f := range zr.File {
			name := safeName(f.Name, dir)
			if count++; name == "" || !f.Mode().IsRegular() || count > maxEntries {
				return "", fmt.Errorf("%s: refused entry %q", archive, f.Name)
			}
			r, err := f.Open()
			if err != nil {
				return "", err
			}
			err = write(name, r)
			r.Close()
			if err != nil {
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
		}
		return filepath.Join(dest, dir), nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return filepath.Join(dest, dir), nil
		}
		if err != nil {
			return "", err
		}
		name := safeName(h.Name, dir)
		if count++; name == "" || h.Typeflag != tar.TypeReg || count > maxEntries {
			return "", fmt.Errorf("%s: refused entry %q", archive, h.Name)
		}
		if err := write(name, tr); err != nil {
			return "", fmt.Errorf("%s: %w", h.Name, err)
		}
	}
}

// runResult is what one started program did.
type runResult struct {
	Out  string
	Code int
}

// runProgram starts exe with args in env, a var so a test can supply it.
var runProgram = func(ctx context.Context, exe string, env []string, args ...string) (runResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return runResult{out.String(), ee.ExitCode()}, nil
	}
	return runResult{out.String(), 0}, err
}

// minimalEnv is an environment with nothing in it that could help a program
// find its neighbours or a login: PATH is an empty directory, HOME and the
// config folders are empty directories, and on Windows only SystemRoot stays
// (the loader needs it).
func minimalEnv(empty string) []string {
	env := []string{"PATH=" + empty, "HOME=" + empty, "USERPROFILE=" + empty, "XDG_CONFIG_HOME=" + empty, "APPDATA=" + empty, "LOCALAPPDATA=" + empty}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	return env
}

// Smoke runs a release archive the way a user's machine would, with nothing
// but the archive: it checks the archive against SHA256SUMS, unpacks it into
// dest (an empty directory), starts each program with --version under a
// minimal environment, and runs `aiquota status`, which must find the
// provider plugin beside itself: with no Claude Code login the answer is
// UNKNOWN, but never "no go-aiquota-plugin-claude".
func Smoke(ctx context.Context, archive, sums, version, dest string, log io.Writer) error {
	if err := verifySum(archive, sums); err != nil {
		return err
	}
	fmt.Fprintf(log, "checksum ok: %s\n", filepath.Base(archive))
	dir, err := extract(archive, dest)
	if err != nil {
		return err
	}
	empty := filepath.Join(dest, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		return err
	}
	env := minimalEnv(empty)
	exe := func(p Program) string {
		return filepath.Join(dir, exeName(p, Target{GOOS: runtime.GOOS}))
	}
	for _, p := range Programs {
		r, err := runProgram(ctx, exe(p), env, "--version")
		if err != nil {
			return fmt.Errorf("%s --version: %w", p.Name, err)
		}
		want := p.Name + " " + version
		if p.VersionVar == "" {
			want = p.Name + " v" // the plugin reports its own module's version
		}
		if r.Code != 0 || !strings.HasPrefix(strings.TrimSpace(r.Out), want) {
			return fmt.Errorf("%s --version: exit %d, output %q, want it to start with %q", p.Name, r.Code, r.Out, want)
		}
		fmt.Fprintf(log, "%s", r.Out)
	}
	r, err := runProgram(ctx, exe(Programs[0]), env, "status", "--fresh")
	if err != nil {
		return fmt.Errorf("aiquota status: %w", err)
	}
	if strings.Contains(r.Out, "no go-aiquota-plugin-claude") {
		return fmt.Errorf("aiquota status did not find its plugin beside it:\n%s", r.Out)
	}
	if r.Code != 0 && r.Code != 1 && r.Code != 3 {
		return fmt.Errorf("aiquota status: exit %d:\n%s", r.Code, r.Out)
	}
	fmt.Fprintf(log, "aiquota status: exit %d, plugin found\n", r.Code)
	return nil
}

func smokeCmd() *cobra.Command {
	var archive, sums, version, dest string
	c := &cobra.Command{
		Use:   "smoke",
		Short: "run a release archive on this machine: checksum, --version, aiquota status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := Smoke(cmd.Context(), archive, sums, version, dest, cmd.OutOrStdout()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "smoke ok")
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&archive, "archive", "", "the release archive (.tar.gz or .zip)")
	f.StringVar(&sums, "sums", "", "the SHA256SUMS file")
	f.StringVar(&version, "version", "", "the release tag the programs must report")
	f.StringVar(&dest, "dest", "", "an empty directory to unpack into")
	for _, n := range []string{"archive", "sums", "version", "dest"} {
		_ = c.MarkFlagRequired(n)
	}
	return c
}
