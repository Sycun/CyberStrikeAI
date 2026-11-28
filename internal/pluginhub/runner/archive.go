package runner

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxUnpacked = 1 << 30
const maxArchiveEntries = 15000

type unpacker struct {
	ctx       context.Context
	root      *os.Root
	strip     int
	remaining int64
	entries   int
}

func extract(ctx context.Context, path, destination, format string, strip int) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	extractor := unpacker{ctx: ctx, root: root, strip: strip, remaining: maxUnpacked}
	if format == "zip" {
		reader, err := zip.OpenReader(path)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, entry := range reader.File {
			if entry.UncompressedSize64 > maxUnpacked {
				return fmt.Errorf("archive entry exceeds extraction limit")
			}
			stream, err := entry.Open()
			if err != nil {
				return err
			}
			err = extractor.write(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), stream)
			stream.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if format != "tar_gz" {
		return fmt.Errorf("unsupported release archive")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	stream, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer stream.Close()
	reader := tar.NewReader(stream)
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive contains an unsupported entry")
		}
		if err := extractor.write(entry.Name, entry.FileInfo().Mode(), entry.Size, reader); err != nil {
			return err
		}
	}
}

func (u *unpacker) write(name string, mode fs.FileMode, size int64, content io.Reader) error {
	if err := u.ctx.Err(); err != nil {
		return err
	}
	u.entries++
	if u.entries > maxArchiveEntries || size < 0 || size > u.remaining {
		return fmt.Errorf("archive extraction limit exceeded")
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if name == "" && mode.IsDir() {
		return nil
	}
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:") || len(name) > 512 {
		return fmt.Errorf("archive contains an invalid path")
	}
	if !mode.IsRegular() && !mode.IsDir() {
		return fmt.Errorf("archive contains an unsupported entry")
	}
	if u.strip == 1 {
		_, child, present := strings.Cut(name, "/")
		if !present && mode.IsDir() {
			return nil
		}
		if !present {
			return fmt.Errorf("archive entry lacks expected root directory")
		}
		name = child
	}
	filename, err := filepath.Localize(name)
	if err != nil {
		return fmt.Errorf("archive path unsupported on this host")
	}
	if mode.IsDir() {
		return u.root.MkdirAll(filename, 0700)
	}
	parent := path.Dir(name)
	if parent != "." {
		if err := u.root.MkdirAll(filepath.FromSlash(parent), 0700); err != nil {
			return err
		}
	}
	permission := fs.FileMode(0600) | mode.Perm()&0100
	output, err := u.root.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(output, io.LimitReader(content, size+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || n != size {
		return fmt.Errorf("archive entry length or write failed")
	}
	u.remaining -= n
	return nil
}
