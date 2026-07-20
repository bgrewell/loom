// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/netpath"
)

// originFixture serves an origin with a known ladder in the background and
// returns a plain client + base URL against it.
func originFixture(t *testing.T, params map[string]string) (*http.Client, string, app.Server) {
	t.Helper()
	cn, sn := memPair(t)
	srv := buildServer(t, sn, params)
	ctx, cancel := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-srvDone; err != nil {
			t.Errorf("server Run: %v", err)
		}
	})
	hc := &http.Client{Transport: &http.Transport{DialContext: cn.DialContext}}
	return hc, fmt.Sprintf("http://localhost:%d", srv.Addr().Port()), srv
}

// parseMasterM3U8 is the minimal HLS master sanity-parser: returns
// bandwidth → variant URI, failing the test on grammar violations.
func parseMasterM3U8(t *testing.T, body string) map[int]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 || lines[0] != "#EXTM3U" {
		t.Fatalf("master playlist must start with #EXTM3U, got %q", lines[0])
	}
	variants := map[int]string{}
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "#EXT-X-STREAM-INF:") {
			continue
		}
		attrs := strings.TrimPrefix(lines[i], "#EXT-X-STREAM-INF:")
		var bw int
		for _, a := range strings.Split(attrs, ",") {
			if v, ok := strings.CutPrefix(a, "BANDWIDTH="); ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					t.Fatalf("bad BANDWIDTH %q", v)
				}
				bw = n
			}
		}
		if bw == 0 {
			t.Fatalf("variant without BANDWIDTH: %q", lines[i])
		}
		if i+1 >= len(lines) || strings.HasPrefix(lines[i+1], "#") {
			t.Fatalf("EXT-X-STREAM-INF %q not followed by a URI line", lines[i])
		}
		variants[bw] = lines[i+1]
	}
	return variants
}

// TestManifestAndSegments: the generated HLS ladder parses, every variant's
// media playlist lists the advertised segments, and each segment's size is
// exactly kbps·seg_duration/8 with deterministic content. The DASH MPD covers
// the same ladder.
func TestManifestAndSegments(t *testing.T) {
	hc, base, _ := originFixture(t, map[string]string{
		"ladder":       "240p:400k,480p:1200k,720p:2500k",
		"seg_duration": "4s",
		"segments":     "5",
	})

	body, resp := get(t, hc, base+"/media/test/manifest.m3u8")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest.m3u8: status %s", resp.Status)
	}
	variants := parseMasterM3U8(t, string(body))
	wantKbps := []int{400, 1200, 2500}
	if len(variants) != len(wantKbps) {
		t.Fatalf("master lists %d variants, want %d: %v", len(variants), len(wantKbps), variants)
	}
	for _, kbps := range wantKbps {
		uri, ok := variants[kbps*1000]
		if !ok {
			t.Fatalf("master missing BANDWIDTH=%d: %v", kbps*1000, variants)
		}
		if want := fmt.Sprintf("%d/playlist.m3u8", kbps); uri != want {
			t.Fatalf("variant URI = %q, want %q", uri, want)
		}

		// Media playlist: VOD, 5 segments of 4s, ENDLIST-terminated.
		pl, resp := get(t, hc, fmt.Sprintf("%s/media/test/%d/playlist.m3u8", base, kbps))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("playlist %d: status %s", kbps, resp.Status)
		}
		pls := string(pl)
		if !strings.HasPrefix(pls, "#EXTM3U\n") || !strings.Contains(pls, "#EXT-X-ENDLIST") {
			t.Errorf("playlist %d malformed:\n%s", kbps, pls)
		}
		if got := strings.Count(pls, "#EXTINF:"); got != 5 {
			t.Errorf("playlist %d lists %d segments, want 5", kbps, got)
		}

		// Segment sizing: kbps·1000·4s / 8 bytes, exact, deterministic.
		wantSize := int64(kbps) * 1000 * 4 / 8
		seg, resp := get(t, hc, fmt.Sprintf("%s/media/test/%d/seg0", base, kbps))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("seg0 @%d: status %s", kbps, resp.Status)
		}
		if int64(len(seg)) != wantSize || resp.ContentLength != wantSize {
			t.Errorf("seg0 @%d: %d bytes (Content-Length %d), want %d", kbps, len(seg), resp.ContentLength, wantSize)
		}
		seg2, _ := get(t, hc, fmt.Sprintf("%s/media/test/%d/seg0", base, kbps))
		if sha256.Sum256(seg) != sha256.Sum256(seg2) {
			t.Errorf("seg0 @%d not deterministic", kbps)
		}
	}

	// Unknown rendition, segment beyond the playlist, malformed names: 404.
	for _, p := range []string{
		"/media/test/999/seg0",
		"/media/test/400/seg5",
		"/media/test/400/seg-1",
		"/media/test/400/chunk0",
	} {
		if _, resp := get(t, hc, base+p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, resp.StatusCode)
		}
	}

	// DASH: same ladder in the MPD, template matching the segment endpoint.
	mpd, resp := get(t, hc, base+"/media/test/manifest.mpd")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest.mpd: status %s", resp.Status)
	}
	mps := string(mpd)
	if !strings.Contains(mps, "<MPD ") || !strings.Contains(mps, `media="$RepresentationID$/seg$Number$"`) {
		t.Errorf("MPD missing skeleton/template:\n%s", mps)
	}
	for _, kbps := range wantKbps {
		if !strings.Contains(mps, fmt.Sprintf(`bandwidth="%d"`, kbps*1000)) {
			t.Errorf("MPD missing bandwidth=%d", kbps*1000)
		}
	}
	if !strings.Contains(mps, `duration="4000"`) {
		t.Errorf("MPD segment duration not 4000ms:\n%s", mps)
	}
}

// TestServerErrorsCounted: 404s land in the origin's Errors counter.
func TestServerErrorsCounted(t *testing.T) {
	hc, base, srv := originFixture(t, nil)
	if _, resp := get(t, hc, base+"/nosuch"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nosuch: %d, want 404", resp.StatusCode)
	}
	waitFor(t, 5*time.Second, "404 accounted", func() bool {
		h := srv.(*origin).rec.Cumulative()
		return h.Requests == 1 && h.Errors == 1
	})
}

// TestServerPortRange: the origin binds inside port_min..port_max, refuses an
// exhausted range, and Close releases the port (the Configure-then-Destroy
// path).
func TestServerPortRange(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	first, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "47000", "port_max": "47000"}})
	if err != nil {
		t.Fatalf("first server: %v", err)
	}
	if p := first.Addr().Port(); p != 47000 {
		t.Fatalf("bound %d, want 47000", p)
	}
	if _, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "47000", "port_max": "47000"}}); err == nil {
		t.Fatal("second server bound an exhausted range")
	}
	if err := first.(*origin).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	third, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "47000", "port_max": "47000"}})
	if err != nil {
		t.Fatalf("rebind after Close: %v", err)
	}
	if p := third.Addr().Port(); p != 47000 {
		t.Fatalf("rebound on %d, want 47000", p)
	}
	_ = third.(*origin).Close()
}

// TestServerRunHonorsDeadline: Run returns nil promptly when its context
// (the flow's duration bound) expires.
func TestServerRunHonorsDeadline(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	srv := buildServer(t, nb, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := srv.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v after a 50ms deadline", elapsed)
	}
}
