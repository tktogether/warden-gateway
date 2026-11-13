package githubproxy

import "testing"

func testMatcher() *hostMatcher {
	return newHostMatcher([]string{
		"github.com",
		"api.github.com",
		"*.github.com",
		"*.githubusercontent.com",
	})
}

func TestHandlesPath(t *testing.T) {
	h := &Handler{matcher: testMatcher()}
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"explicit https", "/https://github.com/a/b", true},
		{"explicit http", "/http://x.y/a", true},
		{"short github", "/github.com/a", true},
		{"short api", "/api.github.com/x", true},
		{"short subdomain suffix", "/raw.githubusercontent.com/a/b", true},
		{"favicon", "/favicon.svg", false},
		{"panel path", "/warden/api/cn", false},
		{"root", "/", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.HandlesPath(tc.path); got != tc.want {
				t.Fatalf("HandlesPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		rawQuery string
		want     string
		wantErr  bool
	}{
		{
			name:     "explicit https",
			path:     "/https://github.com/tktogether/llmio/info/refs",
			rawQuery: "service=git-upload-pack",
			want:     "https://github.com/tktogether/llmio/info/refs?service=git-upload-pack",
		},
		{
			name:     "short path",
			path:     "/github.com/tktogether/llmio/info/refs",
			rawQuery: "service=git-upload-pack",
			want:     "https://github.com/tktogether/llmio/info/refs?service=git-upload-pack",
		},
		{
			name: "short path without trailing segment",
			path: "/github.com",
			want: "https://github.com/",
		},
		{
			name:    "explicit non-standard port",
			path:    "/http://example.com:8080/x",
			wantErr: true,
		},
		{
			name:    "explicit user info",
			path:    "/https://user:pw@github.com/x",
			wantErr: true,
		},
		{
			name:    "short form with port",
			path:    "/github.com:8443/x",
			wantErr: true,
		},
		{
			name:    "short form with user info",
			path:    "/user@github.com/x",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTarget(tc.path, tc.rawQuery)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseTarget(%q) = %v, want error", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTarget(%q) error: %v", tc.path, err)
			}
			if got.String() != tc.want {
				t.Fatalf("parseTarget(%q) = %q, want %q", tc.path, got.String(), tc.want)
			}
		})
	}
}
