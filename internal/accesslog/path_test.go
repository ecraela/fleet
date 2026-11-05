package accesslog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func pathPolicy(t *testing.T, raw string) *PathPolicy {
	t.Helper()
	p, err := parsePathPolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPathPolicyWithholdsUnknownRoutesAndRedactsBeforeExport(t *testing.T) {
	sanitized := pathPolicy(t, `{"version":1,"mode":"sanitized","routes":["/healthz","/orders/:id","/files/*rest"]}`)
	exact := pathPolicy(t, `{"version":1,"mode":"exact","redact":["/reset/:token","/private/*rest"]}`)
	for _, tc := range []struct {
		policy          *PathPolicy
		raw, path, kind string
	}{
		{nil, "/orders/private?token=secret", "", "withheld"},
		{sanitized, "/healthz?auth=secret", "/healthz", "template"},
		{sanitized, "/orders/user%40example.test?token=secret", "/orders/:id", "template"},
		{sanitized, "/orders/987#secret", "/orders/:id", "template"},
		{sanitized, "/files/a/b/c", "/files/*rest", "template"},
		{sanitized, "/password/private-token", "", "unmatched"},
		{sanitized, "/orders/", "", "unmatched"},
		{exact, "/reset/private-token?secret=hidden", "/reset/:token", "template"},
		{exact, "/private/token/another-secret", "/private/*rest", "template"},
		{exact, "/catalog/sale?token=secret", "/catalog/sale", "exact"},
	} {
		p, k := tc.policy.capture(tc.raw)
		if p != tc.path || k != tc.kind {
			t.Fatalf("got %q/%s, want %q/%s", p, k, tc.path, tc.kind)
		}
	}
	for _, raw := range []string{"/reset/private%2ftoken", "/reset/private%252ftoken", "/reset/%zz", "/reset/%0a", "/reset/%3fsecret", "/reset/%5csecret", "/reset/..", "//reset/token", "/reset/hello world", "https://example.test/token", "/reset/é", "/" + strings.Repeat("a", MaxPath)} {
		if p, k := exact.capture(raw); p != "" || k != "invalid" {
			t.Fatalf("unsafe path escaped: %q/%s", p, k)
		}
	}
}

func TestPathPolicyRequiresExplicitBoundedLocalAuthority(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"mode":"exact"}`, `{"version":1,"mode":"automatic"}`,
		`{"version":1,"mode":"exact","routes":["/orders/:id"]}`,
		`{"version":1,"mode":"sanitized","redact":["/orders/:id"]}`,
		`{"version":1,"mode":"sanitized","routes":["/a","/a"]}`,
		`{"version":1,"mode":"sanitized","routes":["/a/*rest/extra"]}`,
		`{"version":1,"mode":"sanitized","routes":["/a/:bad-name"]}`,
		`{"version":1,"mode":"sanitized","routes":["/a?token=secret"]}`,
		`{"version":1,"mode":"exact","url":"https://external.invalid"}`,
		`{"version":1,"mode":"exact"} {}`,
	} {
		if _, err := parsePathPolicy([]byte(raw)); err == nil {
			t.Fatal("invalid path policy accepted")
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "paths.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"mode":"sanitized","routes":["/orders/:id"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPathPolicy(file)
	if err != nil || !domain.ValidDigest(p.digest) {
		t.Fatal("local policy unavailable", err)
	}
	if err = os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPathPolicy(file); err == nil {
		t.Fatal("untrusted writable policy accepted")
	}
	if _, err = LoadPathPolicy("relative.json"); err == nil {
		t.Fatal("relative policy accepted")
	}
}

func TestCV2SanitizesLocallyAndKeepsLegacyRecordsReadable(t *testing.T) {
	p := pathPolicy(t, `{"version":1,"mode":"sanitized","routes":["/orders/:id"]}`)
	boot, now := domain.NewID(), time.Now()
	rawPath := "/orders/private-user?token=private-query"
	line := strings.Replace(sampleLine(), "cv1 ", "cv2 ", 1) + fmt.Sprintf(" %d %s", len(rawPath), hex.EncodeToString([]byte(rawPath)))
	e, err := parse([]byte(line), boot, 1, now, p)
	if err != nil || e.Path != "/orders/:id" || e.PathKind != "template" {
		t.Fatal(e, err)
	}
	wire, _ := json.Marshal(e)
	if strings.Contains(string(wire), "private") || strings.Contains(string(wire), "token") {
		t.Fatal("raw request escaped the agent")
	}
	if legacy, err := Parse([]byte(sampleLine()), boot, 2, now); err != nil || legacy.Path != "" || legacy.PathKind != "" {
		t.Fatal("legacy logs broken", err)
	}
	if withheld, err := Parse([]byte(line), boot, 3, now); err != nil || withheld.Path != "" || withheld.PathKind != "withheld" {
		t.Fatal("missing local opt-in exported paths", err)
	}
	truncated := strings.TrimSuffix(line, line[len(line)-4:])
	if invalid, err := parse([]byte(truncated), boot, 4, now, p); err != nil || invalid.Path != "" || invalid.PathKind != "invalid" {
		t.Fatal("truncated path emitted", err)
	}
}
