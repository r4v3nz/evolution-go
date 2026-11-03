// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): tests for
// wa_version.go and the WhatsApp Web version lookup. Nothing here reaches the
// network: sw.js is served by httptest on 127.0.0.1.

package whatsmeow_service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/store"
)

// resetWebVersionCache clears the lookup cache (success and failure) and
// restores it, the lookup URL and whatsmeow's version when the test ends.
func resetWebVersionCache(t *testing.T) {
	t.Helper()
	cachedWebVersionMu.Lock()
	cachedWebVersion, webVersionErr = nil, nil
	cachedWebVersionMu.Unlock()
	url, before := webVersionURL, store.GetWAVersion()
	t.Cleanup(func() {
		cachedWebVersionMu.Lock()
		cachedWebVersion, webVersionErr = nil, nil
		cachedWebVersionMu.Unlock()
		webVersionURL = url
		applyWAVersion(before)
	})
}

func serveSW(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	webVersionURL = srv.URL + "/sw.js"
	return &hits
}

func TestChooseWAVersion(t *testing.T) {
	b := builtInWAVersion
	newer := &clientVersion{Major: int(b[0]), Minor: int(b[1]), Patch: int(b[2]) + 1}
	older := &clientVersion{Major: int(b[0]), Minor: int(b[1]), Patch: int(b[2]) - 1}
	cases := []struct {
		name        string
		pinned, web *clientVersion
		want        store.WAVersionContainer
		wantSource  string
	}{
		{"nothing: built-in", nil, nil, b, "whatsmeow built-in"},
		{"newer web version wins", nil, newer, newer.container(), "WhatsApp Web"},
		{"older web version never downgrades", nil, older, b, "whatsmeow built-in"},
		{"same web version: built-in", nil, &clientVersion{int(b[0]), int(b[1]), int(b[2])}, b, "whatsmeow built-in"},
		{"pin wins even when older", older, newer, older.container(), "WHATSAPP_VERSION_*"},
	}
	for _, c := range cases {
		got, source := chooseWAVersion(c.pinned, c.web)
		if got != c.want || source != c.wantSource {
			t.Errorf("%s: got %s (%s), want %s (%s)", c.name, got, source, c.want, c.wantSource)
		}
	}
}

// The bug of 2026-11-03: the chosen version must reach the login payload,
// not only the companion device props.
func TestApplyWAVersionReachesLoginPayload(t *testing.T) {
	resetWebVersionCache(t)
	v := store.WAVersionContainer{2, 3000, builtInWAVersion[2] + 12345}
	applyWAVersion(v)

	if got := store.GetWAVersion(); got != v {
		t.Fatalf("store.GetWAVersion() = %s, want %s", got, v)
	}
	av := store.BaseClientPayload.GetUserAgent().GetAppVersion()
	if av.GetPrimary() != v[0] || av.GetSecondary() != v[1] || av.GetTertiary() != v[2] {
		t.Fatalf("login payload carries %d.%d.%d, want %s", av.GetPrimary(), av.GetSecondary(), av.GetTertiary(), v)
	}
	dp := store.DeviceProps.GetVersion()
	if dp.GetPrimary() != v[0] || dp.GetSecondary() != v[1] || dp.GetTertiary() != v[2] {
		t.Fatalf("device props carry %d.%d.%d, want %s", dp.GetPrimary(), dp.GetSecondary(), dp.GetTertiary(), v)
	}
}

func TestWebVersionLookupParsesRevision(t *testing.T) {
	resetWebVersionCache(t)
	hits := serveSW(t, http.StatusOK, `self.__swData=JSON.parse(/*BTDS*/"{\"dynamic_data\":{\"SiteData\":[],\"client_revision\":1052556849}}");`)

	v, err := fetchWhatsAppWebVersion("")
	if err != nil {
		t.Fatal(err)
	}
	if *v != (clientVersion{2, 3000, 1052556849}) {
		t.Fatalf("got %+v", *v)
	}
	if _, err := fetchWhatsAppWebVersion(""); err != nil || hits.Load() != 1 {
		t.Fatalf("second lookup should come from the cache: err=%v hits=%d", err, hits.Load())
	}
}

// sw.js answering an error page (HTTP 400 on 2026-11-03): a clear error, and
// no new request until webVersionRetryAfter has passed.
func TestWebVersionLookupErrorStatusIsCachedBriefly(t *testing.T) {
	resetWebVersionCache(t)
	hits := serveSW(t, http.StatusBadRequest, "<html>Sorry, something went wrong.</html>")

	_, err := fetchWhatsAppWebVersion("")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("want an HTTP 400 error, got %v", err)
	}
	if _, err := fetchWhatsAppWebVersion(""); err == nil || hits.Load() != 1 {
		t.Fatalf("a failed lookup must not be repeated at once: err=%v hits=%d", err, hits.Load())
	}

	cachedWebVersionMu.Lock()
	webVersionErrAt = time.Now().Add(-webVersionRetryAfter - time.Second)
	cachedWebVersionMu.Unlock()
	_, _ = fetchWhatsAppWebVersion("")
	if hits.Load() != 2 {
		t.Fatalf("after webVersionRetryAfter the lookup must be tried again: hits=%d", hits.Load())
	}
}
