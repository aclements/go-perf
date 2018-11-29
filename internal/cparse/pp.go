// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cparse

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type BuildEnv struct {
	CCArgs []string
}

var macroRe = regexp.MustCompile(`^#define ([_a-zA-Z][_a-zA-Z0-9]*)`)

// FindMacros returns the names of all macros defined by the C source
// in r.
func FindMacros(env *BuildEnv, r io.Reader) ([]string, error) {
	ccArgs := append([]string(nil), env.CCArgs...)
	ccArgs = append(ccArgs, "-x", "c", "-E", "-dM", "-")
	cc := exec.Command("cc", ccArgs...)
	cc.Stdin = r
	cc.Stderr = os.Stderr
	out, err := cc.Output()
	if err != nil {
		return nil, err
	}
	var macros []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		m := macroRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("failed to parse macro %q", line)
		}
		macros = append(macros, m[1])
	}
	return macros, nil
}

// Preprocess invokes the C preprocessor to pre-process the C source
// in r.
func Preprocess(env *BuildEnv, r io.Reader) ([]byte, error) {
	// Invoke C compiler for pre-processing.
	ccArgs := append([]string(nil), env.CCArgs...)
	ccArgs = append(ccArgs, "-x", "c", "-E", "-")
	cc := exec.Command("cc", ccArgs...)
	cc.Stdin = r
	cc.Stderr = os.Stderr
	return cc.Output()
}
