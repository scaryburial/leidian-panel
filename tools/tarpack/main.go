// Command tarpack builds the release tarball with correct Unix permission bits.
//
// Why this exists: the release package must carry mode 0755 on the binary and
// the scripts. Windows' bundled tar.exe cannot store Unix modes (every entry
// comes out as rw-rw-rw-) and does not support --mode, so a package built on
// Windows would extract without the executable bit. The bundled install.sh does
// chmod +x, so a normal install still works, but anyone who unpacks the package
// and runs ./ui3344 directly hits "Permission denied" — and a release artifact
// should not depend on a workaround to be usable.
//
// Usage: go run ./tools/tarpack <source-dir> <output.tar.gz>
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// execMode reports the mode an entry should carry inside the archive.
func execMode(rel string, isDir bool) int64 {
	if isDir {
		return 0o755
	}
	base := filepath.Base(rel)
	switch {
	case base == "ui3344": // the panel binary itself
		return 0o755
	case strings.HasSuffix(base, ".sh"), strings.HasSuffix(base, ".py"):
		return 0o755
	case filepath.Base(filepath.Dir(rel)) == "bin": // bundled xray core
		return 0o755
	}
	return 0o644
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tarpack <source-dir> <output.tar.gz>")
		os.Exit(2)
	}
	src, out := os.Args[1], os.Args[2]

	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "tarpack: %s is not a directory\n", src)
		os.Exit(1)
	}

	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tarpack: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	// Fixed timestamp keeps repeated builds of identical input byte-identical.
	gz.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)

	root := filepath.Base(src)
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(src), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != root && !strings.HasPrefix(rel, root+"/") {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Mode = execMode(rel, d.IsDir())
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "root", "root"
		hdr.ModTime = time.Unix(0, 0)
		hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	if walkErr != nil {
		fmt.Fprintf(os.Stderr, "tarpack: %v\n", walkErr)
		os.Exit(1)
	}
	if err := tw.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "tarpack: %v\n", err)
		os.Exit(1)
	}
	if err := gz.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "tarpack: %v\n", err)
		os.Exit(1)
	}
}
