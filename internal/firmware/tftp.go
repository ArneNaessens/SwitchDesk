package firmware

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TFTP is a read-only TFTP server (RFC 1350 with the blksize, tsize and
// timeout options of RFC 2347-2349). It only serves files the resolve
// function allows, and refuses all writes.
type TFTP struct {
	conn    *net.UDPConn
	resolve func(name string) (path string, ok bool)
	// Progress is called during transfers with bytes sent so far.
	Progress func(remote, name string, sent, total int64)
	// Done is called when a transfer ends; err is nil on success.
	Done func(remote, name string, err error)

	wg     sync.WaitGroup
	closed chan struct{}
}

const (
	opRRQ   = 1
	opWRQ   = 2
	opDATA  = 3
	opACK   = 4
	opERROR = 5
	opOACK  = 6
)

// StartTFTP listens on addr (normally ":69").
func StartTFTP(addr string, resolve func(string) (string, bool)) (*TFTP, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp4", ua)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "Only one usage") {
			return nil, fmt.Errorf("UDP port %d is taken by another TFTP server on this computer; stop it and try again", ua.Port)
		}
		if strings.Contains(err.Error(), "permission denied") {
			return nil, fmt.Errorf("this computer won't let SwitchDesk open UDP port %d", ua.Port)
		}
		return nil, err
	}
	s := &TFTP{conn: c, resolve: resolve, closed: make(chan struct{})}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

func (s *TFTP) Port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

func (s *TFTP) Close() {
	select {
	case <-s.closed:
		return
	default:
	}
	close(s.closed)
	s.conn.Close()
	s.wg.Wait()
}

func (s *TFTP) loop() {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, remote, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				continue
			}
		}
		if n < 4 {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		switch binary.BigEndian.Uint16(pkt) {
		case opRRQ:
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(remote, pkt[2:])
			}()
		case opWRQ:
			sendError(s.conn, remote, 2, "this server is read-only")
		}
	}
}

func sendError(c *net.UDPConn, to *net.UDPAddr, code uint16, msg string) {
	b := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(b, opERROR)
	binary.BigEndian.PutUint16(b[2:], code)
	b = append(append(b, msg...), 0)
	if to == nil {
		c.Write(b)
	} else {
		c.WriteToUDP(b, to)
	}
}

func (s *TFTP) serve(remote *net.UDPAddr, req []byte) {
	parts := bytes.Split(req, []byte{0})
	if len(parts) < 2 {
		return
	}
	name := strings.TrimLeft(string(parts[0]), "/")
	opts := map[string]string{}
	for i := 2; i+1 < len(parts); i += 2 {
		if len(parts[i]) > 0 {
			opts[strings.ToLower(string(parts[i]))] = string(parts[i+1])
		}
	}
	// each transfer gets its own socket, as the RFC asks
	c, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		return
	}
	defer c.Close()
	p, ok := s.resolve(name)
	if !ok {
		sendError(c, nil, 1, "file not found")
		s.done(remote, name, errors.New("switch asked for a file that isn't being offered: "+name))
		return
	}
	f, err := os.Open(p)
	if err != nil {
		sendError(c, nil, 1, "file not found")
		s.done(remote, name, err)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()

	blk := 512
	timeout := 3 * time.Second
	var oack []string
	if v, ok := opts["blksize"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 {
			if n > 1428 { // stay under a 1500 MTU
				n = 1428
			}
			blk = n
			oack = append(oack, "blksize", strconv.Itoa(n))
		}
	}
	if _, ok := opts["tsize"]; ok {
		oack = append(oack, "tsize", strconv.FormatInt(size, 10))
	}
	if v, ok := opts["timeout"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 255 {
			timeout = time.Duration(n) * time.Second
			oack = append(oack, "timeout", v)
		}
	}

	ack := make([]byte, 1500)
	// send a packet and wait for the ACK of block want
	exchange := func(pkt []byte, want uint16) error {
		for try := 0; try < 6; try++ {
			if _, err := c.Write(pkt); err != nil {
				return err
			}
			deadline := time.Now().Add(timeout)
			for {
				select {
				case <-s.closed:
					return errors.New("server stopped")
				default:
				}
				c.SetReadDeadline(deadline)
				n, err := c.Read(ack)
				if err != nil {
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						break // retransmit
					}
					return err
				}
				if n < 4 {
					continue
				}
				switch binary.BigEndian.Uint16(ack) {
				case opACK:
					if binary.BigEndian.Uint16(ack[2:]) == want {
						return nil
					}
					// stale duplicate ACK: keep waiting
				case opERROR:
					return fmt.Errorf("switch aborted the transfer: %s", strings.TrimRight(string(ack[4:n]), "\x00"))
				}
			}
		}
		return errors.New("switch stopped answering during the transfer")
	}

	if len(oack) > 0 {
		pkt := []byte{0, opOACK}
		for _, o := range oack {
			pkt = append(append(pkt, o...), 0)
		}
		if err := exchange(pkt, 0); err != nil {
			s.done(remote, name, err)
			return
		}
	}

	data := make([]byte, 4+blk)
	binary.BigEndian.PutUint16(data, opDATA)
	var block uint16 = 1 // wraps past 65535, which switches expect for large images
	var sent int64
	last := time.Now()
	for {
		n, rerr := io.ReadFull(f, data[4:])
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			sendError(c, nil, 0, "read error")
			s.done(remote, name, rerr)
			return
		}
		binary.BigEndian.PutUint16(data[2:], block)
		if err := exchange(data[:4+n], block); err != nil {
			s.done(remote, name, err)
			return
		}
		sent += int64(n)
		if s.Progress != nil && (time.Since(last) > 500*time.Millisecond || n < blk) {
			s.Progress(remote.IP.String(), name, sent, size)
			last = time.Now()
		}
		if n < blk {
			s.done(remote, name, nil)
			return
		}
		block++
	}
}

func (s *TFTP) done(remote *net.UDPAddr, name string, err error) {
	if s.Done != nil {
		s.Done(remote.IP.String(), name, err)
	}
}

// LocalIPFor returns this computer's address on the route to host, which is
// the address the switch should download from.
func LocalIPFor(host string) (string, error) {
	c, err := net.Dial("udp4", net.JoinHostPort(host, "69"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
