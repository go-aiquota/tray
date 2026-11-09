// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type scanHit struct {
	file, hit, context string
	c                  class
}

// scanTranscripts reads (read-only) every dir/*/*.jsonl, classifies each
// ASSISTANT message and returns the totals and every match.
func scanTranscripts(dir string) (files, messages int, hits []scanHit, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	if err != nil {
		return 0, 0, nil, err
	}
	for _, p := range paths {
		f, oerr := os.Open(p)
		if oerr != nil {
			continue
		}
		files++
		assistantTexts(f, func(msg string) {
			messages++
			if c, h, s := classify(msg); c != classNone {
				hits = append(hits, scanHit{file: filepath.Base(p), c: c, hit: h, context: oneLine(s, 200)})
			}
		})
		_ = f.Close()
	}
	return files, messages, hits, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func scanCmd() *cobra.Command {
	var dir string
	var sample int
	c := &cobra.Command{
		Use:   "scan",
		Short: "Run the matcher over real transcripts (read-only) and print counts and an evenly spaced sample",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".claude", "projects")
			}
			files, msgs, hits, err := scanTranscripts(dir)
			if err != nil {
				return err
			}
			return printScan(cmd.OutOrStdout(), files, msgs, hits, sample)
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "transcripts root (default ~/.claude/projects)")
	c.Flags().IntVar(&sample, "sample", 20, "how many matches to print, evenly spaced")
	return c
}

func printScan(w io.Writer, files, msgs int, hits []scanHit, sample int) error {
	fmt.Fprintf(w, "files %d, assistant messages %d, matches %d\n", files, msgs, len(hits))
	if len(hits) == 0 || sample <= 0 {
		return nil
	}
	step := len(hits) / sample
	if step < 1 {
		step = 1
	}
	n := 0
	for i := 0; i < len(hits) && n < sample; i += step {
		h := hits[i]
		kind := "general"
		if h.c == classExhausted {
			kind = "exhausted"
		}
		fmt.Fprintf(w, "[%d] %s %s %q :: %s\n", i, h.file, kind, h.hit, h.context)
		n++
	}
	return nil
}
