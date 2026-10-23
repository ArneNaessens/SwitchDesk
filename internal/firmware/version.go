// Package firmware handles Aruba switch images: version strings, the local
// image library, and a small TFTP server switches pull images from.
package firmware

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Version is an Aruba software version such as WC.16.11.0015 or FL.10.13.1000.
// Branch is the platform code; images only fit switches with the same branch.
type Version struct {
	Branch string `json:"branch"`
	Nums   []int  `json:"nums"`
	Text   string `json:"text"`
}

var versionRe = regexp.MustCompile(`\b([A-Z]{2})[._](1[0-9])[._](\d{1,2})[._](\d{4})\b`)

// ParseVersion pulls the first Aruba version out of s ("WC.16.11.0015",
// "WC_16_11_0015.swi", "Software revision : FL.10.13.1000 ...").
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	v := Version{Branch: m[1]}
	for _, p := range m[2:] {
		n, _ := strconv.Atoi(p)
		v.Nums = append(v.Nums, n)
	}
	v.Text = fmt.Sprintf("%s.%02d.%02d.%04d", v.Branch, v.Nums[0], v.Nums[1], v.Nums[2])
	return v, true
}

// Compare returns -1, 0 or 1. Versions of different branches compare as 0.
func (v Version) Compare(o Version) int {
	if v.Branch != o.Branch {
		return 0
	}
	for i := 0; i < len(v.Nums) && i < len(o.Nums); i++ {
		if v.Nums[i] != o.Nums[i] {
			if v.Nums[i] < o.Nums[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v Version) Newer(o Version) bool { return v.Branch == o.Branch && v.Compare(o) > 0 }

// cxPlatforms maps the platform names used in AOS-CX image file names to the
// branch code the switch reports. Used only when the file itself doesn't say.
var cxPlatforms = []struct {
	names  []string
	branch string
}{
	{[]string{"6400", "6300"}, "FL"},
	{[]string{"6200"}, "ML"},
	{[]string{"6100", "6000", "4100i"}, "PL"},
	{[]string{"8320"}, "TL"},
	{[]string{"8325"}, "GL"},
	{[]string{"8360"}, "LL"},
	{[]string{"8400"}, "XL"},
	{[]string{"10000"}, "DL"},
	{[]string{"9300"}, "TL"},
}

var cxFileRe = regexp.MustCompile(`(?i)ArubaOS-CX_([0-9a-z_-]+?)_(10)_(\d{2})_(\d{4})`)

// DetectImage works out which version an image file contains: from a version
// in the file name, then from AOS-CX platform names in the file name, then by
// scanning the start of the file.
func DetectImage(path string) (Version, error) {
	name := filepath.Base(path)
	if v, ok := ParseVersion(name); ok {
		return v, nil
	}
	if m := cxFileRe.FindStringSubmatch(name); m != nil {
		for _, p := range cxPlatforms {
			for _, n := range p.names {
				if strings.Contains(m[1], n) {
					return ParseVersionMust(fmt.Sprintf("%s.%s.%s.%s", p.branch, m[2], m[3], m[4])), nil
				}
			}
		}
	}
	// last resort: AOS-S images carry their version near the start of the file
	if v, ok := scanFile(path, 64<<20); ok {
		return v, nil
	}
	return Version{}, fmt.Errorf("can't tell which switches %s is for; rename it like WC_16_11_0015.swi", name)
}

func ParseVersionMust(s string) Version {
	v, _ := ParseVersion(s)
	return v
}

// scanFile looks for a version string in the first limit bytes.
func scanFile(path string, limit int64) (Version, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Version{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, limit), 1<<20)
	buf := make([]byte, 1<<20)
	carry := []byte{}
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			if m := versionRe.Find(chunk); m != nil {
				return ParseVersion(string(m))
			}
			if len(chunk) > 32 {
				carry = append([]byte{}, chunk[len(chunk)-32:]...)
			}
		}
		if err != nil {
			return Version{}, false
		}
	}
}
