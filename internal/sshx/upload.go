package sshx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// UploadProgress reports how many bytes have gone out so far.
type UploadProgress func(sent, total int64)

// Upload pushes a file to the switch over a separate SSH connection, so the
// switch never has to reach back to us (no TFTP, works through VPNs and zero
// trust tunnels). It tries SFTP first and falls back to the legacy SCP
// protocol. It returns which of the two was used.
//
// AOS-S only accepts this when "ip ssh filetransfer" is on, and only for
// special targets such as /os/primary and /os/secondary.
func Upload(ctx context.Context, o Options, remote string, r io.Reader, size int64, progress UploadProgress) (string, error) {
	client, err := dialClient(ctx, o)
	if err != nil {
		return "", err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()

	// SFTP needs a fresh reader if it fails half way, so only fall back to
	// SCP when SFTP couldn't even start.
	sc, err := sftp.NewClient(client, sftp.UseConcurrentWrites(false))
	if err == nil {
		defer sc.Close()
		if err := sftpPut(sc, remote, r, size, progress); err != nil {
			return "sftp", fmt.Errorf("sftp upload to %s: %w", remote, err)
		}
		return "sftp", nil
	}
	sftpErr := err
	if err := scpPut(client, remote, r, size, progress); err != nil {
		return "scp", fmt.Errorf("sftp unavailable (%v), scp upload to %s: %w", sftpErr, remote, err)
	}
	return "scp", nil
}

// dialClient opens a plain SSH client (no PTY shell) with the same algorithms
// and host key pinning as Dial.
func dialClient(ctx context.Context, o Options) (*ssh.Client, error) {
	if o.Port == 0 {
		o.Port = 22
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	var seen string
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	d := net.Dialer{Timeout: o.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig(o, &seen))
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrHostKeyMismatch) || strings.Contains(err.Error(), ErrHostKeyMismatch.Error()) {
			return nil, ErrHostKeyMismatch
		}
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

const chunk = 32 * 1024

func copyWithProgress(dst io.Writer, src io.Reader, size int64, progress UploadProgress) (int64, error) {
	buf := make([]byte, chunk)
	var sent int64
	last := time.Time{}
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return sent, err
			}
			sent += int64(n)
			if progress != nil && (time.Since(last) > 250*time.Millisecond || sent == size) {
				progress(sent, size)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			return sent, nil
		}
		if rerr != nil {
			return sent, rerr
		}
	}
}

func sftpPut(sc *sftp.Client, remote string, r io.Reader, size int64, progress UploadProgress) error {
	f, err := sc.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	sent, err := copyWithProgress(f, r, size, progress)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && size > 0 && sent != size {
		err = fmt.Errorf("sent %d of %d bytes", sent, size)
	}
	return err
}

// scpPut speaks the sink side of the classic rcp/scp protocol ("scp -t").
func scpPut(client *ssh.Client, remote string, r io.Reader, size int64, progress UploadProgress) error {
	s, err := client.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	w, err := s.StdinPipe()
	if err != nil {
		return err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		return err
	}
	ack := bufio.NewReader(out)
	if err := s.Start("scp -t " + remote); err != nil {
		return err
	}
	if err := scpAck(ack); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "C0644 %d %s\n", size, path.Base(remote)); err != nil {
		return err
	}
	if err := scpAck(ack); err != nil {
		return err
	}
	if _, err := copyWithProgress(w, r, size, progress); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return err
	}
	if err := scpAck(ack); err != nil {
		return err
	}
	w.Close()
	return nil
}

func scpAck(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("no reply from the switch: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	return fmt.Errorf("switch refused the file: %s", strings.TrimSpace(msg))
}
