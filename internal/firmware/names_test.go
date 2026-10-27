package firmware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Files copied into the library by hand with names a switch CLI or the UI
// can't take safely are renamed, never indexed under the raw name.
func TestHandPlacedUnsafeNamesAreRenamed(t *testing.T) {
	dir := t.TempDir()
	bad := []string{
		"WC_16_11_0021\nboot set-default flash secondary\n.swi",
		"WC_16_11_0015'+(window.__pwned=1)+'.swi",
		"WC_16_10_0012 copy.swi",
	}
	for _, n := range bad {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("image header WC.16.11.0021 "), 0o600); err != nil {
			t.Skip("file system doesn't allow this name:", err)
		}
	}
	l, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Rescan()
	imgs := l.List()
	if len(imgs) != len(bad) {
		t.Fatalf("want %d images, got %+v", len(bad), imgs)
	}
	for _, img := range imgs {
		if !safeFile.MatchString(img.File) || strings.Contains(img.File, "..") {
			t.Fatalf("unsafe name indexed: %q", img.File)
		}
		if _, err := l.Path(img.File); err != nil {
			t.Fatalf("indexed name not usable: %q %v", img.File, err)
		}
	}
}

func TestCheckName(t *testing.T) {
	for _, n := range []string{"x..swi", "index.json", "a b.swi", "x\n.swi", "", ".", ".."} {
		if _, err := checkName(n); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
	if got, err := checkName("a/b.swi"); err != nil || got != "b.swi" {
		t.Errorf("a/b.swi -> %q %v", got, err)
	}
	if _, err := checkName("WC_16_11_0021.swi"); err != nil {
		t.Error(err)
	}
}
