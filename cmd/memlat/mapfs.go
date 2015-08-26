// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

type mapFS map[string]*mapFSFile

func (m mapFS) Open(name string) (http.File, error) {
	f, ok := m[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return mapFSReader{strings.NewReader(f.content), f, m}, nil
}

type mapFSReader struct {
	*strings.Reader
	info *mapFSFile
	fs   mapFS
}

func (mapFSReader) Close() error {
	return nil
}

func (m mapFSReader) Readdir(count int) ([]os.FileInfo, error) {
	if !m.info.IsDir() {
		return nil, os.ErrInvalid
	}
	out := []os.FileInfo{}
	for _, file := range m.fs {
		if !strings.HasPrefix(file.name, m.info.name) {
			continue
		}
		rel := file.name[len(m.info.name)+1:]
		nsep := strings.Count(rel, "/")
		if nsep == 0 || nsep == 1 && strings.HasSuffix(rel, "/") {
			out = append(out, file)
		}
	}
	return out, nil
}

func (m mapFSReader) Stat() (os.FileInfo, error) {
	return m.info, nil
}

type mapFSFile struct {
	name    string
	modTime time.Time
	isDir   bool
	content string
}

func (f *mapFSFile) Name() string {
	return path.Base(f.name)
}

func (f *mapFSFile) Size() int64 {
	return int64(len(f.content))
}

func (f *mapFSFile) Mode() os.FileMode {
	return 0777
}

func (f *mapFSFile) ModTime() time.Time {
	return f.modTime
}

func (f *mapFSFile) IsDir() bool {
	return f.isDir
}

func (f *mapFSFile) Sys() interface{} {
	return nil
}
