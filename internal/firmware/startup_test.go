package firmware

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opening the library must not read the images (issue #7: a big or odd
// firmware folder kept the window blank at startup). New files are picked
// up by Rescan, and files whose version can't be found are remembered so
// they aren't read again on every scan.
func TestOpenDoesNotScanAndUnrecognizedIsRemembered(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "WC_16_11_0021.swi")
	junk := filepath.Join(dir, "notes.swi")
	os.WriteFile(good, []byte("image header WC.16.11.0021 "), 0o600)
	os.WriteFile(junk, []byte("nothing useful in here"), 0o600)

	l, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(l.List()); n != 0 {
		t.Fatalf("OpenLibrary indexed %d files; it should only load the saved index", n)
	}

	l.Rescan()
	if _, ok := l.Get("WC_16_11_0021.swi"); !ok {
		t.Fatal("Rescan didn't index the image")
	}
	if u := l.Unrecognized(); len(u) != 1 || u[0] != "notes.swi" {
		t.Fatalf("unrecognized = %v, want [notes.swi]", u)
	}

	// Reopen: both lists come back from disk without reading the files.
	l2, _ := OpenLibrary(dir)
	if _, ok := l2.Get("WC_16_11_0021.swi"); !ok {
		t.Fatal("index not saved")
	}
	if u := l2.Unrecognized(); len(u) != 1 {
		t.Fatalf("unrecognized list not saved: %v", u)
	}

	// Once the file changes it is looked at again.
	os.WriteFile(junk, []byte("image header WC.16.10.0012 "), 0o600)
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(junk, later, later)
	l2.Rescan()
	if _, ok := l2.Get("notes.swi"); !ok {
		t.Fatal("changed file wasn't scanned again")
	}
	if u := l2.Unrecognized(); len(u) != 0 {
		t.Fatalf("unrecognized = %v, want none", u)
	}

	// A deleted file drops out of both lists.
	os.Remove(good)
	l2.Rescan()
	if _, ok := l2.Get("WC_16_11_0021.swi"); ok {
		t.Fatal("removed file still indexed")
	}
}
