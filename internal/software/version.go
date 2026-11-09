// Package software validates signed Fleet release metadata used by the manager
// and the independent host updater. It never treats a reported version as proof
// that the executable was signed.
package software

import (
	"errors"
	"strconv"
	"strings"
)

type version struct {
	parts [3]uint64
	pre   []string
}

func parseVersion(raw string) (version, error) {
	var out version
	if len(raw) < 6 || len(raw) > 64 || raw[0] != 'v' || strings.Contains(raw, "+") {
		return out, errors.New("invalid exact Fleet version")
	}
	base, pre, hasPre := strings.Cut(raw[1:], "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return out, errors.New("invalid exact Fleet version")
	}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' || !digits(part) {
			return out, errors.New("invalid exact Fleet version")
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return out, errors.New("invalid exact Fleet version")
		}
		out.parts[i] = value
	}
	if hasPre {
		if pre == "" {
			return out, errors.New("invalid exact Fleet version")
		}
		out.pre = strings.Split(pre, ".")
		for _, part := range out.pre {
			if part == "" || len(part) > 32 {
				return out, errors.New("invalid exact Fleet version")
			}
			for _, ch := range part {
				if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch == '-') {
					return out, errors.New("invalid exact Fleet version")
				}
			}
			if digits(part) && len(part) > 1 && part[0] == '0' {
				return out, errors.New("invalid exact Fleet version")
			}
		}
	}
	return out, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func compare(a, b version) int {
	for i := range a.parts {
		if a.parts[i] < b.parts[i] {
			return -1
		}
		if a.parts[i] > b.parts[i] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) > 0 {
		return 1
	}
	if len(a.pre) > 0 && len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < min(len(a.pre), len(b.pre)); i++ {
		x, y := a.pre[i], b.pre[i]
		if x == y {
			continue
		}
		if digits(x) && digits(y) {
			if len(x) < len(y) || len(x) == len(y) && x < y {
				return -1
			}
			return 1
		}
		if digits(x) {
			return -1
		}
		if digits(y) {
			return 1
		}
		if x < y {
			return -1
		}
		return 1
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func ValidVersion(value string) bool {
	_, err := parseVersion(value)
	return err == nil
}

func Compare(a, b string) (int, error) {
	x, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	y, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	return compare(x, y), nil
}

type Range struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

func (r Range) Contains(value string) bool {
	low, err1 := parseVersion(r.Min)
	high, err2 := parseVersion(r.Max)
	actual, err3 := parseVersion(value)
	return err1 == nil && err2 == nil && err3 == nil && compare(low, high) <= 0 && compare(low, actual) <= 0 && compare(actual, high) <= 0
}

type Compatibility struct {
	Manager Range `json:"manager"`
	Agent   Range `json:"agent"`
}

func (c Compatibility) Valid(candidate string) bool {
	return c.Manager.Contains(candidate) && c.Agent.Contains(candidate)
}
