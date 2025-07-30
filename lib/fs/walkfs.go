// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This part copied directly from golang.org/src/path/filepath/path.go (Go
// 1.6) and lightly modified to be methods on BasicFilesystem.

// In our Walk() all paths given to a WalkFunc() are relative to the
// filesystem root.

package fs

import (
	"errors"
	"os"
	"unsafe"
)

var ErrInfiniteRecursion = errors.New("infinite filesystem recursion detected")

type ancestorDirList struct {
	list []FileInfo
	fs   Filesystem
}

func (ancestors *ancestorDirList) Push(info FileInfo) {
	l.Debugf("ancestorDirList: Push '%s'", info.Name())
	ancestors.list = append(ancestors.list, info)
}

func (ancestors *ancestorDirList) Pop() FileInfo {
	aLen := len(ancestors.list)
	info := ancestors.list[aLen-1]
	l.Debugf("ancestorDirList: Pop '%s'", info.Name())
	ancestors.list = ancestors.list[:aLen-1]
	return info
}

func (ancestors *ancestorDirList) Contains(info FileInfo) bool {
	l.Debugf("ancestorDirList: Contains '%s'", info.Name())
	for _, ancestor := range ancestors.list {
		if ancestors.fs.SameFile(info, ancestor) {
			return true
		}
	}
	return false
}

type Path struct {
	buf        []byte
	separators []int
	str        string
}

const separator byte = os.PathSeparator

func NewPath(path string) *Path {
	buf := []byte(path)
	separators := make([]int, 0, 10)
	for idx, c := range buf {
		if c == separator {
			separators = append(separators, idx)
		}
	}
	return &Path{
		buf:        buf,
		separators: separators,
		str:        path,
	}
}

func NewEmptyPath() *Path {
	return &Path{
		buf:        make([]byte, 0, 100),
		separators: make([]int, 0, 10),
	}
}

// Unsafe for performance reasons, always copy if not immediately used.
func (p *Path) String() string {
	return p.str
}

// Safe, copy version.
func (p *Path) StringCopy() string {
	return string(p.buf)
}

func (p *Path) Push(elem string) {
	if len(p.separators) > 0 {
		p.separators = append(p.separators, len(p.buf))
		p.buf = append(p.buf, separator)
	}
	p.buf = append(p.buf, elem...)
	p.str = unsafe.String(unsafe.SliceData(p.buf), len(p.buf))
}

func (p *Path) Pop() {
	if len(p.separators) == 0 {
		return
	}
	p.buf = p.buf[:p.separators[len(p.separators)-1]]
	p.separators = p.separators[:len(p.separators)-1]
	p.str = unsafe.String(unsafe.SliceData(p.buf), len(p.buf))
}

func (p *Path) Canonicalize() error {
	path, err := Canonicalize(p.String())
	if err != nil {
		return err
	}
	if path == p.String() {
		return nil
	}
	p.buf = []byte(path)
	p.separators = make([]int, 0, len(p.separators))
	for idx, c := range p.buf {
		if c == separator {
			p.separators = append(p.separators, idx)
		}
	}
	p.str = unsafe.String(unsafe.SliceData(p.buf), len(p.buf))
	return nil
}

// WalkFunc is the type of the function called for each file or directory
// visited by Walk. The path argument contains the argument to Walk as a
// prefix; that is, if Walk is called with "dir", which is a directory
// containing the file "a", the walk function will be called with argument
// "dir/a". The info argument is the FileInfo for the named path.
//
// If there was a problem walking to the file or directory named by path, the
// incoming error will describe the problem and the function can decide how
// to handle that error (and Walk will not descend into that directory). If
// an error is returned, processing stops. The sole exception is when the function
// returns the special value SkipDir. If the function returns SkipDir when invoked
// on a directory, Walk skips the directory's contents entirely.
// If the function returns SkipDir when invoked on a non-directory file,
// Walk skips the remaining files in the containing directory.
type WalkFunc func(path *Path, info FileInfo, err error) error

type walkFilesystem struct {
	Filesystem
	checkInfiniteRecursion bool
}

func NewWalkFilesystem(next Filesystem) Filesystem {
	fs := &walkFilesystem{
		Filesystem: next,
	}
	for _, opt := range next.Options() {
		if _, ok := opt.(*OptionJunctionsAsDirs); ok {
			fs.checkInfiniteRecursion = true
			break
		}
	}
	return fs
}

// walk recursively descends path, calling walkFn.
func (f *walkFilesystem) walk(path *Path, info FileInfo, walkFn WalkFunc, ancestors *ancestorDirList) error {
	l.Debugf("walk: path=%s", path)
	err := path.Canonicalize()
	if err != nil {
		return err
	}

	err = walkFn(path, info, nil)
	if err != nil {
		if info.IsDir() && errors.Is(err, SkipDir) {
			return nil
		}
		return err
	}

	if !info.IsDir() && path.String() != "." {
		return nil
	}

	if f.checkInfiniteRecursion {
		if !ancestors.Contains(info) {
			ancestors.Push(info)
			defer ancestors.Pop()
		} else {
			return walkFn(path, info, ErrInfiniteRecursion)
		}
	}

	names, err := f.DirNames(path.String())
	if err != nil {
		return walkFn(path, info, err)
	}

	for _, name := range names {
		path.Push(name)
		fileInfo, err := f.Lstat(path.String())
		if err != nil {
			if err := walkFn(path, fileInfo, err); err != nil && !errors.Is(err, SkipDir) {
				return err
			}
		} else {
			err = f.walk(path, fileInfo, walkFn, ancestors)
			if err != nil {
				if !fileInfo.IsDir() || !errors.Is(err, SkipDir) {
					return err
				}
			}
		}
	}
	return nil
}

// Walk walks the file tree rooted at root, calling walkFn for each file or
// directory in the tree, including root. All errors that arise visiting files
// and directories are filtered by walkFn. The files are walked in lexical
// order, which makes the output deterministic but means that for very
// large directories Walk can be inefficient.
// Walk does not follow symbolic links.
func (f *walkFilesystem) Walk(root *Path, walkFn WalkFunc) error {
	info, err := f.Lstat(root.String())
	if err != nil {
		return walkFn(root, nil, err)
	}
	var ancestors *ancestorDirList
	if f.checkInfiniteRecursion {
		ancestors = &ancestorDirList{fs: f.Filesystem}
	}
	return f.walk(root, info, walkFn, ancestors)
}

func (f *walkFilesystem) underlying() (Filesystem, bool) {
	return f.Filesystem, true
}
