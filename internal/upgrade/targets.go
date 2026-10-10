package upgrade

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Targets are the versions a cluster should run after upgrading to Minor.
// An empty add-on version means "no known target": the check then reports the
// running version as WARN instead of guessing.
type Targets struct {
	Minor     int    `json:"minor"`
	KubeProxy string `json:"kube_proxy,omitempty"`
	CoreDNS   string `json:"coredns,omitempty"`
	VPCCNI    string `json:"vpc_cni,omitempty"`
	Source    string `json:"source"`
}

// TargetsAsOf is when the built-in table was last checked against the AWS EKS
// user guide ("latest version of the Amazon EKS add-on type for each
// Kubernetes version"). Override with flags when AWS ships a newer build.
const TargetsAsOf = "2026-10-10"

// LatestKnownMinor is the default --target.
const LatestKnownMinor = 36

var kubeProxyLatest = map[int]string{
	36: "v1.36.0-eksbuild.21",
	35: "v1.35.3-eksbuild.25",
	34: "v1.34.6-eksbuild.25",
	33: "v1.33.10-eksbuild.25",
	32: "v1.32.13-eksbuild.28",
	31: "v1.31.14-eksbuild.32",
}

var coreDNSLatest = map[int]string{
	36: "v1.14.3-eksbuild.3",
}

// The VPC CNI is not tied to a Kubernetes minor: one version covers 1.31-1.37.
const vpcCNILatest = "v1.23.1-eksbuild.1"

// DefaultTargets returns the built-in targets for a Kubernetes minor.
func DefaultTargets(minor int) Targets {
	t := Targets{
		Minor:     minor,
		KubeProxy: kubeProxyLatest[minor],
		CoreDNS:   coreDNSLatest[minor],
		Source:    "built-in table, AWS EKS docs as of " + TargetsAsOf,
	}
	if minor >= 31 && minor <= 37 {
		t.VPCCNI = vpcCNILatest
	}
	return t
}

// ParseMinor accepts "1.36", "36", "v1.36" or "v1.36.2-eks-x".
func ParseMinor(s string) (int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if m := minorRE.FindStringSubmatch(s); m != nil {
		return strconv.Atoi(m[1])
	}
	return 0, fmt.Errorf("%q is not a Kubernetes version (want e.g. 1.36)", s)
}

var minorRE = regexp.MustCompile(`^1\.(\d+)`)

// minorOf returns the minor of a version string, or -1.
func minorOf(v string) int {
	m := minorRE.FindStringSubmatch(strings.TrimPrefix(v, "v"))
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// shortVersion trims the build suffix: v1.36.2-eks-1a2b3c -> v1.36.2.
func shortVersion(v string) string {
	if i := strings.Index(v, "-eks-"); i > 0 {
		return v[:i]
	}
	return v
}

// imageTag returns the tag of an image reference, ignoring any @digest.
// "repo:5000/kube-proxy:v1.36.0-eksbuild.21@sha256:..." -> "v1.36.0-eksbuild.21".
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	slash := strings.LastIndex(image, "/")
	if i := strings.LastIndex(image, ":"); i > slash {
		return image[i+1:]
	}
	return ""
}

// compareVersions orders EKS add-on versions such as v1.14.3-eksbuild.3:
// numeric segments compare as numbers, so eksbuild.21 > eksbuild.3.
// Returns -1, 0 or 1.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y part
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if c := x.cmp(y); c != 0 {
			return c
		}
	}
	return 0
}

type part struct {
	num   int
	isNum bool
	str   string
}

func (p part) cmp(q part) int {
	switch {
	case p.isNum && q.isNum:
		switch {
		case p.num < q.num:
			return -1
		case p.num > q.num:
			return 1
		}
		return 0
	case p.isNum != q.isNum:
		// a number sorts after a missing segment, and a string vs number is ordered by kind
		if !p.isNum && p.str == "" {
			return -1
		}
		if !q.isNum && q.str == "" {
			return 1
		}
		if p.isNum {
			return 1
		}
		return -1
	}
	return strings.Compare(p.str, q.str)
}

var segRE = regexp.MustCompile(`\d+|[A-Za-z]+`)

func versionParts(v string) []part {
	var out []part
	for _, s := range segRE.FindAllString(strings.TrimPrefix(v, "v"), -1) {
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, part{num: n, isNum: true})
		} else {
			out = append(out, part{str: s})
		}
	}
	return out
}
