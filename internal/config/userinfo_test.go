package config

import "testing"

func TestParseSubUserInfo(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   SubUserInfo
		found  bool
	}{
		{
			name:   "as providers send it",
			header: "upload=455727941; download=6174313016; total=1073741824000; expire=1773203698",
			want:   SubUserInfo{Upload: 455727941, Download: 6174313016, Total: 1073741824000, Expire: 1773203698},
			found:  true,
		},
		{
			name:   "spaces and case",
			header: "Upload=1; DOWNLOAD=2 ; total = 3 ; expire=4",
			want:   SubUserInfo{Upload: 1, Download: 2, Total: 3, Expire: 4},
			found:  true,
		},
		{
			name:   "the fields a provider cares about only",
			header: "total=100",
			want:   SubUserInfo{Total: 100},
			found:  true,
		},
		{
			// Some panels report the expiry in milliseconds, which would show
			// up as a date tens of thousands of years away.
			name:   "expiry in milliseconds",
			header: "expire=1773203698000",
			want:   SubUserInfo{Expire: 1773203698},
			found:  true,
		},
		{
			name:   "unlimited",
			header: "upload=10; download=20; total=0; expire=0",
			want:   SubUserInfo{Upload: 10, Download: 20},
			found:  true,
		},
		{
			name:   "keys we do not know",
			header: "reset_day=1; upload=5",
			want:   SubUserInfo{Upload: 5},
			found:  true,
		},
		{name: "nothing usable", header: "hello; upload=abc", found: false},
		{name: "empty", header: "", found: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := ParseSubUserInfo(tc.header)
			if found != tc.found {
				t.Fatalf("found = %v, want %v", found, tc.found)
			}
			if got != tc.want {
				t.Fatalf("info = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSubUserInfoArithmetic(t *testing.T) {
	if (SubUserInfo{}).Known() {
		t.Fatal("a provider that reported nothing must not look known")
	}
	if !(SubUserInfo{Total: 1}).Known() {
		t.Fatal("a reported total is something to show")
	}

	half := SubUserInfo{Upload: 10, Download: 30, Total: 100}
	if half.Used() != 40 {
		t.Fatalf("used = %d", half.Used())
	}
	if half.Remaining() != 60 {
		t.Fatalf("remaining = %d", half.Remaining())
	}

	// Going over the quota must not produce a negative remainder, which the
	// panels would render as a negative number of bytes.
	over := SubUserInfo{Upload: 80, Download: 80, Total: 100}
	if over.Remaining() != 0 {
		t.Fatalf("remaining = %d", over.Remaining())
	}

	// An unlimited plan reports no total at all.
	if (SubUserInfo{Upload: 5}).Remaining() != 0 {
		t.Fatal("an unlimited plan has no remainder to compute")
	}
}
