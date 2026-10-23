package firmware

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	a, _ := ParseVersion("Software revision  : WC.16.10.0012")
	b, _ := ParseVersion("WC_16_11_0015.swi")
	c, _ := ParseVersion("FL.10.13.1000")
	if a.Text != "WC.16.10.0012" || b.Text != "WC.16.11.0015" || !b.Newer(a) || a.Newer(b) || c.Newer(a) {
		t.Fatal(a, b, c)
	}
	if _, ok := ParseVersion("hello"); ok {
		t.Fatal("parsed garbage")
	}
}

func TestDetectImage(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, body, 0o600)
		return p
	}
	if v, err := DetectImage(write("YA_16_11_0021.swi", []byte("x"))); err != nil || v.Text != "YA.16.11.0021" {
		t.Fatal(v, err)
	}
	if v, err := DetectImage(write("ArubaOS-CX_6400-6300_10_13_1000.swi", []byte("x"))); err != nil || v.Text != "FL.10.13.1000" {
		t.Fatal(v, err)
	}
	if v, err := DetectImage(write("ArubaOS-CX_6100-6000_10_14_0001.swi", []byte("x"))); err != nil || v.Branch != "PL" {
		t.Fatal(v, err)
	}
	body := append(make([]byte, 5000), []byte("....KB.16.11.0020....")...)
	if v, err := DetectImage(write("renamed.swi", body)); err != nil || v.Text != "KB.16.11.0020" {
		t.Fatal(v, err)
	}
	if _, err := DetectImage(write("mystery.swi", []byte("nothing"))); err == nil {
		t.Fatal("expected failure")
	}
}

func TestLibrary(t *testing.T) {
	src := filepath.Join(t.TempDir(), "WC_16_11_0015.swi")
	os.WriteFile(src, []byte("image-bytes"), 0o600)
	lib, err := OpenLibrary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	img, err := lib.AddFile(src)
	if err != nil || img.Version.Text != "WC.16.11.0015" || img.SHA256 == "" {
		t.Fatal(img, err)
	}
	// a file dropped into the folder by hand is picked up on rescan
	os.WriteFile(filepath.Join(lib.Dir(), "WC_16_11_0021.swi"), []byte("newer"), 0o600)
	lib.Rescan()
	if best, ok := lib.Latest("WC"); !ok || best.Version.Text != "WC.16.11.0021" {
		t.Fatal(best)
	}
	if _, err := lib.Path("../etc/passwd"); err == nil {
		t.Fatal("path escape allowed")
	}

	// download, honouring Content-Disposition
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/expired" {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>sign in</html>"))
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="FL_10_14_0001.swi"`)
		w.Write(bytes.Repeat([]byte("z"), 300000))
	}))
	defer srv.Close()
	var lastDone int64
	got, err := lib.Download(context.Background(), srv.URL+"/dl?id=1", func(d, total int64) { lastDone = d })
	if err != nil || got.File != "FL_10_14_0001.swi" || got.Size != 300000 || lastDone != 300000 {
		t.Fatal(got, err, lastDone)
	}
	if _, err := lib.Download(context.Background(), srv.URL+"/expired", nil); err == nil {
		t.Fatal("html accepted as firmware")
	}
	if err := lib.Delete("FL_10_14_0001.swi"); err != nil || len(lib.List()) != 2 {
		t.Fatal(err, lib.List())
	}
}

// fetch is a minimal TFTP client used to test the server.
func fetch(t *testing.T, port int, name string, blksize int) ([]byte, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	req := []byte{0, opRRQ}
	req = append(append(append(req, name...), 0), "octet\x00"...)
	if blksize > 0 {
		req = append(append(req, "blksize\x00"...), strconv.Itoa(blksize)+"\x00"...)
	}
	c.WriteToUDP(req, srv)
	var out bytes.Buffer
	buf := make([]byte, 70000)
	size := 512
	var expect uint16 = 1
	for {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		switch binary.BigEndian.Uint16(buf) {
		case opERROR:
			return nil, fmt.Errorf("error %s", buf[4:n-1])
		case opOACK:
			size = blksize
			c.WriteToUDP([]byte{0, opACK, 0, 0}, from)
		case opDATA:
			blk := binary.BigEndian.Uint16(buf[2:])
			ack := []byte{0, opACK, buf[2], buf[3]}
			c.WriteToUDP(ack, from)
			if blk != expect {
				continue
			}
			out.Write(buf[4:n])
			expect++
			if n-4 < size {
				return out.Bytes(), nil
			}
		}
	}
}

func TestTFTP(t *testing.T) {
	dir := t.TempDir()
	small := make([]byte, 512*3) // exact multiple: needs an empty final block
	rand.Read(small)
	big := make([]byte, 8*70000+3) // > 65535 blocks at blksize 8: block numbers wrap
	rand.Read(big)
	os.WriteFile(filepath.Join(dir, "a.swi"), small, 0o600)
	os.WriteFile(filepath.Join(dir, "b.swi"), big, 0o600)
	s, err := StartTFTP("127.0.0.1:0", func(n string) (string, bool) {
		if n == "a.swi" || n == "b.swi" {
			return filepath.Join(dir, n), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var doneErr error = fmt.Errorf("not called")
	s.Done = func(_, _ string, err error) { doneErr = err }

	got, err := fetch(t, s.Port(), "a.swi", 0)
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("plain transfer: %v (%d bytes)", err, len(got))
	}
	got, err = fetch(t, s.Port(), "b.swi", 8)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("rollover transfer: %v (%d of %d bytes)", err, len(got), len(big))
	}
	time.Sleep(50 * time.Millisecond)
	if doneErr != nil {
		t.Fatal("done callback:", doneErr)
	}
	if _, err := fetch(t, s.Port(), "secret.txt", 0); err == nil {
		t.Fatal("served a file that wasn't offered")
	}
}
