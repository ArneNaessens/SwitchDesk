package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Image is one firmware file in the library.
type Image struct {
	File    string    `json:"file"`
	Version Version   `json:"version"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	AddedAt time.Time `json:"addedAt"`
	Source  string    `json:"source"` // "file" or the download host
}

// Library is a folder of images plus an index with their detected versions.
type Library struct {
	dir string
	mu  sync.Mutex
	idx map[string]Image
}

func OpenLibrary(dir string) (*Library, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Library{dir: dir, idx: map[string]Image{}}
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		json.Unmarshal(b, &l.idx)
	}
	l.reconcile()
	return l, nil
}

func (l *Library) Dir() string { return l.dir }

func (l *Library) saveLocked() error {
	b, _ := json.MarshalIndent(l.idx, "", "  ")
	return os.WriteFile(filepath.Join(l.dir, "index.json"), b, 0o600)
}

// reconcile drops index entries whose file is gone and indexes files that
// were copied into the folder by hand.
func (l *Library) reconcile() {
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	for name := range l.idx {
		if _, err := checkName(name); err != nil {
			delete(l.idx, name)
			changed = true
			continue
		}
		if _, err := os.Stat(filepath.Join(l.dir, name)); err != nil {
			delete(l.idx, name)
			changed = true
		}
	}
	ents, _ := os.ReadDir(l.dir)
	for _, e := range ents {
		if !e.Type().IsRegular() || !isImageName(e.Name()) {
			continue
		}
		name, err := checkName(e.Name())
		if err != nil {
			// A file copied in by hand with spaces, quotes, newlines or other
			// characters a switch CLI or the UI can't take safely is renamed
			// to a safe name; if that name is taken it is left out.
			safe, cerr := checkName(cleanName(e.Name()))
			if cerr != nil || !isImageName(safe) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(l.dir, safe)); err == nil {
				continue
			}
			if os.Rename(filepath.Join(l.dir, e.Name()), filepath.Join(l.dir, safe)) != nil {
				continue
			}
			name = safe
		}
		if _, ok := l.idx[name]; ok {
			continue
		}
		if img, err := describe(filepath.Join(l.dir, name), "file"); err == nil {
			l.idx[name] = img
			changed = true
		}
	}
	if changed {
		l.saveLocked()
	}
}

// Rescan picks up files added to or removed from the folder by hand.
func (l *Library) Rescan() { l.reconcile() }

func isImageName(n string) bool {
	return strings.HasSuffix(strings.ToLower(n), ".swi")
}

var safeFile = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,200}$`)

// checkName makes sure an image name is a single safe file name inside the
// library folder: no separators, no "..", only [A-Za-z0-9._+-]. Names reach
// file paths, the switch CLI ("copy tftp ...") and the UI, so every name is
// checked here before use.
func checkName(n string) (string, error) {
	n = filepath.Base(n)
	if n == "." || n == "index.json" || strings.Contains(n, "..") || !safeFile.MatchString(n) {
		return "", errors.New("bad file name")
	}
	return n, nil
}

func describe(p, source string) (Image, error) {
	st, err := os.Stat(p)
	if err != nil {
		return Image{}, err
	}
	v, err := DetectImage(p)
	if err != nil {
		return Image{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return Image{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Image{}, err
	}
	return Image{File: filepath.Base(p), Version: v, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil)), AddedAt: time.Now(), Source: source}, nil
}

// List returns the indexed images, newest first within each branch.
func (l *Library) List() []Image {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Image, 0, len(l.idx))
	for _, img := range l.idx {
		out = append(out, img)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version.Branch != out[j].Version.Branch {
			return out[i].Version.Branch < out[j].Version.Branch
		}
		return out[i].Version.Compare(out[j].Version) > 0
	})
	return out
}

// Get returns the image by file name.
func (l *Library) Get(file string) (Image, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	img, ok := l.idx[file]
	return img, ok
}

// Path returns the full path of an image in the library, refusing anything
// that would escape the folder.
func (l *Library) Path(file string) (string, error) {
	name, err := checkName(file)
	if err != nil || name != file {
		return "", errors.New("bad file name")
	}
	return filepath.Join(l.dir, name), nil
}

// Latest returns the newest image for a branch, if any.
func (l *Library) Latest(branch string) (Image, bool) {
	var best Image
	found := false
	for _, img := range l.List() {
		if img.Version.Branch == branch && (!found || img.Version.Newer(best.Version)) {
			best, found = img, true
		}
	}
	return best, found
}

// AddFile copies a local image into the library.
func (l *Library) AddFile(src string) (Image, error) {
	name, err := checkName(cleanName(filepath.Base(src)))
	if err != nil || !isImageName(name) {
		return Image{}, errors.New("firmware images end in .swi")
	}
	if _, err := DetectImage(src); err != nil {
		return Image{}, err
	}
	dst := filepath.Join(l.dir, name)
	tmp := dst + ".part"
	in, err := os.Open(src)
	if err != nil {
		return Image{}, err
	}
	defer in.Close()
	out, err := os.Create(tmp)
	if err != nil {
		return Image{}, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return Image{}, err
	}
	out.Close()
	return l.finish(tmp, dst, "file")
}

func (l *Library) finish(tmp, dst, source string) (Image, error) {
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return Image{}, err
	}
	img, err := describe(dst, source)
	if err != nil {
		os.Remove(dst)
		return Image{}, err
	}
	l.mu.Lock()
	l.idx[img.File] = img
	l.saveLocked()
	l.mu.Unlock()
	return img, nil
}

func cleanName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r == ' ' {
			return '_'
		}
		return r
	}, n)
	if !safeFile.MatchString(n) {
		n = regexp.MustCompile(`[^A-Za-z0-9._+-]`).ReplaceAllString(n, "_")
	}
	return n
}

// Progress reports download progress; total is -1 when unknown.
type Progress func(done, total int64)

// Download fetches an image from an http(s) link, such as a download link
// copied from the HPE Networking Support Portal.
func (l *Library) Download(ctx context.Context, link string, progress Progress) (Image, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Image{}, errors.New("paste a full http(s) download link")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", "SwitchDesk")
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return Image{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Image{}, fmt.Errorf("download failed: %s (portal links expire after a while; copy a fresh one)", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return Image{}, errors.New("the link returned a web page, not a file; it may need you to be signed in, or it has expired")
	}
	name := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if m := regexp.MustCompile(`filename\*?=(?:UTF-8'')?"?([^";]+)"?`).FindStringSubmatch(cd); m != nil {
			name = path.Base(m[1])
		}
	}
	if name == "" {
		name = path.Base(resp.Request.URL.Path)
	}
	clean := cleanName(name)
	name, err = checkName(clean)
	if err != nil || !isImageName(name) {
		return Image{}, fmt.Errorf("the link points to %q, not a .swi firmware image", clean)
	}
	dst := filepath.Join(l.dir, name)
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return Image{}, err
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return Image{}, werr
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 300*time.Millisecond {
				progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return Image{}, rerr
		}
	}
	out.Close()
	if progress != nil {
		progress(done, total)
	}
	return l.finish(tmp, dst, u.Host)
}

func (l *Library) Delete(file string) error {
	p, err := l.Path(file)
	if err != nil {
		return err
	}
	l.mu.Lock()
	delete(l.idx, file)
	l.saveLocked()
	l.mu.Unlock()
	return os.Remove(p)
}
