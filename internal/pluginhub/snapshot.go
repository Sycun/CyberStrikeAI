package pluginhub

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/pluginhub/runner"
)

const maxSnapshotBytes = 1 << 30
const maxSnapshotFiles = 12000

func snapshotPath(spec runner.Spec) string {
	return filepath.Join(spec.Root, ".rollback-"+spec.VersionID)
}

func snapshotData(ctx context.Context, spec runner.Spec) error {
	source := filepath.Join(spec.Root, "data")
	if err := os.MkdirAll(source, 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(spec.Root, ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err := copyData(ctx, source, temp); err != nil {
		return err
	}
	return os.Rename(temp, snapshotPath(spec))
}

func copyData(ctx context.Context, source, destination string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	remaining, count := int64(maxSnapshotBytes), 0
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("snapshot data contains an unsupported file type")
		}
		count++
		if count > maxSnapshotFiles || info.Size() < 0 || info.Size() > remaining {
			return fmt.Errorf("snapshot data exceeds size or entry limits")
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()&0770|0700)
		}
		in, err := root.Open(name)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0770|0600)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, remaining+1))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return fmt.Errorf("snapshot file changed or could not be copied")
		}
		remaining -= n
		return nil
	})
}

func restoreData(spec runner.Spec) error {
	if err := validSnapshotVersion(spec); err != nil {
		return err
	}
	backup := snapshotPath(spec)
	data := filepath.Join(spec.Root, "data")
	failed := filepath.Join(spec.Root, ".failed-data-"+spec.VersionID)
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		return os.RemoveAll(failed)
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(failed); os.IsNotExist(err) {
		if err := os.Rename(data, failed); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.Rename(backup, data); err != nil {
		return err
	}
	return os.RemoveAll(failed)
}
