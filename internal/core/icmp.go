package core

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// icmpPing sends one echo request with the system ping command and returns the
// round trip in milliseconds. The system command is used because raw ICMP
// sockets need admin rights on Windows and macOS, and ping already has them.
// Tests replace it.
var icmpPing = systemPing

var errNoReply = errors.New("no ping reply")

// pingTime finds the round trip in ping output: "time=12.3 ms", "time<1ms",
// or a translated word on Windows ("Zeit=12ms").
var pingTime = regexp.MustCompile(`[=<]\s?(\d+(?:[.,]\d+)?)\s?ms`)

func systemPing(ctx context.Context, host string, timeout time.Duration) (int, error) {
	ms := int(timeout.Milliseconds())
	secs := strconv.Itoa(max(1, int(timeout.Seconds())))
	var args []string
	switch runtime.GOOS {
	case "windows":
		args = []string{"-n", "1", "-w", strconv.Itoa(ms), host}
	case "darwin":
		args = []string{"-c", "1", "-t", secs, host}
	default:
		args = []string{"-c", "1", "-W", secs, host}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", args...)
	hideWindow(cmd)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	lat := int(time.Since(start).Milliseconds())
	return parsePing(string(out), err, lat)
}

// parsePing decides the result from the command's output. Windows ping exits
// 0 even for "Destination host unreachable" sent by a router, so a reply only
// counts when it carries a TTL.
func parsePing(out string, err error, fallbackMs int) (int, error) {
	if err != nil || !strings.Contains(strings.ToLower(out), "ttl=") {
		if strings.Contains(strings.ToLower(out), "unknown host") || strings.Contains(strings.ToLower(out), "could not find host") ||
			strings.Contains(strings.ToLower(out), "cannot resolve") {
			return 0, errors.New("unknown host")
		}
		return 0, errNoReply
	}
	if m := pingTime.FindStringSubmatch(out); m != nil {
		if f, perr := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64); perr == nil {
			return int(f + 0.5), nil
		}
	}
	return fallbackMs, nil
}
