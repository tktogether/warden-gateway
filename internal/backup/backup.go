package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

const maxArchiveBytes = 512 << 20

var idPattern = regexp.MustCompile(`^backup-[0-9]{8}T[0-9]{6}Z(?:-[0-9]+)?$`)

type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}

type Manifest struct {
	ID           string `json:"id"`
	Created      string `json:"created"`
	CreatedTS    int64  `json:"created_ts"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Files        []File `json:"files"`
	Downloadable bool   `json:"downloadable"`
}

type Manager struct {
	Dir         string
	ConfigPath  string
	DataDir     string
	ServicePath string
	Retain      int
}

func New(dir, configPath, dataDir, servicePath string, retain int) *Manager {
	if retain <= 0 {
		retain = 10
	}
	return &Manager{Dir: dir, ConfigPath: configPath, DataDir: dataDir, ServicePath: servicePath, Retain: retain}
}

func (m *Manager) Create() (*Manifest, error) {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return nil, err
	}
	id := "backup-" + time.Now().UTC().Format("20060102T150405Z")
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(m.Dir, id+".tar.gz")); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("backup-%s-%d", time.Now().UTC().Format("20060102T150405Z"), i)
	}
	archive := filepath.Join(m.Dir, id+".tar.gz")
	tmp := archive + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var files []File
	addFile := func(name, src string, mode os.FileMode) error {
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() > maxArchiveBytes {
			return fmt.Errorf("file %s is too large", src)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Mode = int64(mode.Perm())
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "root", "root"
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, File{Path: name, Size: info.Size(), Mode: mode.Perm().String()})
		return nil
	}
	if err := addFile("config/config.json", m.ConfigPath, 0o600); err != nil {
		return nil, fmt.Errorf("backup config: %w", err)
	}
	acmeDir := filepath.Join(m.DataDir, "acme")
	if _, err := os.Stat(acmeDir); err == nil {
		err = filepath.WalkDir(acmeDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(acmeDir, path)
			if err != nil {
				return err
			}
			return addFile(filepath.ToSlash(filepath.Join("data/acme", rel)), path, 0o600)
		})
		if err != nil {
			return nil, fmt.Errorf("backup acme: %w", err)
		}
	}
	if m.ServicePath != "" {
		if _, err := os.Stat(m.ServicePath); err == nil {
			if err := addFile("systemd/warden-gateway.service", m.ServicePath, 0o644); err != nil {
				return nil, fmt.Errorf("backup service: %w", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, archive); err != nil {
		return nil, err
	}
	sum, size, err := hashFile(archive)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	man := &Manifest{
		ID: id, Created: now.Format(time.RFC3339), CreatedTS: now.Unix(),
		Size: size, SHA256: sum, Files: files, Downloadable: true,
	}
	if err := writeManifest(filepath.Join(m.Dir, id+".json"), man); err != nil {
		return nil, err
	}
	_ = m.Prune()
	return man, nil
}

func (m *Manager) List() ([]Manifest, error) {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Manifest{}, nil
		}
		return nil, err
	}
	out := make([]Manifest, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !idPattern.MatchString(id) {
			continue
		}
		man, err := m.Get(id)
		if err != nil {
			continue
		}
		out = append(out, *man)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedTS > out[j].CreatedTS })
	return out, nil
}

func (m *Manager) Get(id string) (*Manifest, error) {
	if !idPattern.MatchString(id) {
		return nil, errors.New("invalid backup id")
	}
	raw, err := os.ReadFile(filepath.Join(m.Dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return nil, err
	}
	archive := filepath.Join(m.Dir, id+".tar.gz")
	if info, err := os.Stat(archive); err != nil {
		man.Downloadable = false
	} else if info.Size() != man.Size {
		man.Size = info.Size()
		man.Downloadable = true
	}
	return &man, nil
}

func (m *Manager) ArchivePath(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", errors.New("invalid backup id")
	}
	p := filepath.Join(m.Dir, id+".tar.gz")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func (m *Manager) Delete(id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid backup id")
	}
	_ = os.Remove(filepath.Join(m.Dir, id+".tar.gz"))
	_ = os.Remove(filepath.Join(m.Dir, id+".json"))
	return nil
}

func (m *Manager) Prune() error {
	list, err := m.List()
	if err != nil {
		return err
	}
	if len(list) <= m.Retain {
		return nil
	}
	for _, man := range list[m.Retain:] {
		_ = m.Delete(man.ID)
	}
	return nil
}

func (m *Manager) Restore(id string) (*Manifest, error) {
	if !idPattern.MatchString(id) {
		return nil, errors.New("invalid backup id")
	}
	archive, err := m.ArchivePath(id)
	if err != nil {
		return nil, err
	}
	man, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "warden-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := extract(archive, tmp); err != nil {
		return nil, err
	}
	newConfig := filepath.Join(tmp, "config", "config.json")
	if _, err := config.Load(newConfig); err != nil {
		return nil, fmt.Errorf("backup config is invalid: %w", err)
	}
	if _, err := m.Create(); err != nil {
		return nil, fmt.Errorf("pre-restore backup: %w", err)
	}
	if err := copyFile(newConfig, m.ConfigPath, 0o600); err != nil {
		return nil, err
	}
	srcAcme := filepath.Join(tmp, "data", "acme")
	if _, err := os.Stat(srcAcme); err == nil {
		dstAcme := filepath.Join(m.DataDir, "acme")
		if err := os.MkdirAll(dstAcme, 0o700); err != nil {
			return nil, err
		}
		_ = filepath.WalkDir(srcAcme, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, relErr := filepath.Rel(srcAcme, path)
			if relErr != nil {
				return relErr
			}
			return copyFile(path, filepath.Join(dstAcme, rel), 0o600)
		})
	}
	srcUnit := filepath.Join(tmp, "systemd", "warden-gateway.service")
	if m.ServicePath != "" {
		if _, err := os.Stat(srcUnit); err == nil {
			_ = copyFile(srcUnit, m.ServicePath, 0o644)
		}
	}
	return man, nil
}

func extract(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe archive entry %q", hdr.Name)
		}
		if hdr.Size < 0 || hdr.Size > maxArchiveBytes {
			return fmt.Errorf("archive entry too large: %s", hdr.Name)
		}
		total += hdr.Size
		if total > maxArchiveBytes {
			return errors.New("archive is too large")
		}
		target := filepath.Join(dst, name)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, dst)
}

func writeManifest(path string, man *Manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, byte(10))
	return os.WriteFile(path, raw, 0o600)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
