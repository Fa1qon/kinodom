package updates

import (
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Input — база для релиза: файл, имя базы и номер схемы.
type Input struct {
	Path   string
	Pack   string
	Schema int
}

// LoadKey читает закрытый ключ (base64 семени Ed25519) из файла.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ключ подписи: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("ключ подписи испорчен")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// BuildRelease готовит в out файлы релиза: базы (сжатые) или установщик, manifest.json и manifest.json.sig.
func BuildRelease(out, version string, key ed25519.PrivateKey, in []Input) (Manifest, error) {
	m := Manifest{Version: version}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return m, err
	}
	for _, x := range in {
		// Установщик уже сжат — кладётся как есть; базы — gzip (вдвое меньше).
		name, pack := filepath.Base(x.Path), copyFile
		if x.Pack != "app" {
			name, pack = name+".gz", gzipFile
		}
		for _, f := range m.Files {
			if f.Name == name {
				return m, fmt.Errorf("два файла с именем %s: у баз разных схем — разные имена", name)
			}
		}
		dst := filepath.Join(out, name)
		if err := pack(x.Path, dst); err != nil {
			return m, fmt.Errorf("%s: %w", x.Path, err)
		}
		sum, size, err := hashFile(dst)
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, File{Name: name, Pack: x.Pack, Schema: x.Schema, Size: size, SHA256: sum})
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), body, 0o644); err != nil {
		return m, err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, body))
	return m, os.WriteFile(filepath.Join(out, "manifest.json.sig"), []byte(sig), 0o644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
