package internal

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var (
	rePrettyName = regexp.MustCompile(`^PRETTY_NAME=(.*)$`)
	reID         = regexp.MustCompile(`^ID=(.*)$`)
	reVersionID  = regexp.MustCompile(`^VERSION_ID=(.*)$`)
	reUbuntu     = regexp.MustCompile(`[\( ]([\d\.]+)`)
	reCentOS     = regexp.MustCompile(`^CentOS( Linux)? release ([\d\.]+)`)
	reRedHat     = regexp.MustCompile(`[\( ]([\d\.]+)`)
)

// Linux OS information.
type LinuxInfo struct {
	Name    string
	Vendor  string
	Version string
	Release string
}

func GetLinuxInfo() (*LinuxInfo, error) {

	var info LinuxInfo

	f, err := os.Open("/etc/os-release")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		if m := rePrettyName.FindStringSubmatch(s.Text()); m != nil {
			info.Name = strings.Trim(m[1], `"`)
		} else if m := reID.FindStringSubmatch(s.Text()); m != nil {
			info.Vendor = strings.Trim(m[1], `"`)
		} else if m := reVersionID.FindStringSubmatch(s.Text()); m != nil {
			info.Version = strings.Trim(m[1], `"`)
		}
	}

	switch info.Vendor {
	case "debian":
		info.Release = slurpFile("/etc/debian_version")
	case "ubuntu":
		if m := reUbuntu.FindStringSubmatch(info.Name); m != nil {
			info.Release = m[1]
		}
	case "centos":
		if release := slurpFile("/etc/centos-release"); release != "" {
			if m := reCentOS.FindStringSubmatch(release); m != nil {
				info.Release = m[2]
			}
		}
	case "rhel":
		if release := slurpFile("/etc/redhat-release"); release != "" {
			if m := reRedHat.FindStringSubmatch(release); m != nil {
				info.Release = m[1]
			}
		}
		if info.Release == "" {
			if m := reRedHat.FindStringSubmatch(info.Name); m != nil {
				info.Release = m[1]
			}
		}
	}

	return &info, nil
}

func slurpFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}
