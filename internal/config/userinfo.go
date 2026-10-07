package config

import (
	"strconv"
	"strings"
)

// SubUserInfo is the quota a provider reports for a subscription, carried in
// the "subscription-userinfo" response header that Clash and v2rayN read:
//
//	subscription-userinfo: upload=455727941; download=6174313016; total=1073741824000; expire=1773203698
//
// A zero Total means the plan is unlimited, and a zero Expire means it does not
// end. Providers are free to leave numbers out, which is why Known exists: it
// tells "unlimited" apart from "this provider does not report anything".
type SubUserInfo struct {
	Upload   int64 `yaml:"upload" json:"upload"`
	Download int64 `yaml:"download" json:"download"`
	Total    int64 `yaml:"total" json:"total"`
	Expire   int64 `yaml:"expire" json:"expire"`
}

// Used is everything counted against the quota so far.
func (u SubUserInfo) Used() int64 { return u.Upload + u.Download }

// Remaining is what is left of the quota, never negative. It is meaningless
// when Total is zero, which is how an unlimited plan is reported.
func (u SubUserInfo) Remaining() int64 {
	if left := u.Total - u.Used(); left > 0 {
		return left
	}
	return 0
}

// Known reports whether the provider reported anything at all.
func (u SubUserInfo) Known() bool {
	return u != SubUserInfo{}
}

// ParseSubUserInfo reads the header. It reports false when the header carries
// none of the numbers, so the caller can tell an absent header from a provider
// that reports zero usage.
func ParseSubUserInfo(header string) (SubUserInfo, bool) {
	var info SubUserInfo
	found := false
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			info.Upload = number
		case "download":
			info.Download = number
		case "total":
			info.Total = number
		case "expire":
			// The convention is seconds, but a few panels send milliseconds,
			// which would otherwise land tens of thousands of years from now.
			if number > 1e12 {
				number /= 1000
			}
			info.Expire = number
		default:
			continue
		}
		found = true
	}
	return info, found
}
