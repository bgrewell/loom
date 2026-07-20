// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rendition is one ladder rung as learned from the master manifest: identity
// (kbps from BANDWIDTH), the resolved media-playlist URL, and — once that
// playlist has been fetched — the segment list. The origin's manifest is the
// truth; nothing here is derived from parameters.
type rendition struct {
	kbps     int
	label    string
	playlist *url.URL
	segs     []segmentRef // nil until the media playlist is fetched
}

// segmentRef is one media segment: its resolved URL and the EXTINF duration
// the playhead accounting uses (possibly overridden by seg_duration).
type segmentRef struct {
	url *url.URL
	dur time.Duration
}

// parseMaster parses an HLS master playlist into the ladder, resolving each
// variant URI against base and sorting ascending by bitrate. Only the subset
// the HTTPOrigin emits is required: #EXT-X-STREAM-INF lines with a BANDWIDTH
// attribute, each followed by the variant's URI line.
func parseMaster(base *url.URL, body string) ([]rendition, error) {
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return nil, errors.New("vidstream: master manifest is not an m3u8 playlist")
	}
	var (
		out  []rendition
		pend *rendition
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if attrs, ok := strings.CutPrefix(line, "#EXT-X-STREAM-INF:"); ok {
			r := rendition{}
			for _, attr := range splitAttrs(attrs) {
				key, val, ok := strings.Cut(attr, "=")
				if !ok {
					continue
				}
				switch strings.TrimSpace(key) {
				case "BANDWIDTH":
					bw, err := strconv.Atoi(strings.TrimSpace(val))
					if err != nil || bw < 1000 {
						return nil, fmt.Errorf("vidstream: master manifest: bad BANDWIDTH %q", val)
					}
					r.kbps = bw / 1000
				case "NAME":
					r.label = strings.Trim(strings.TrimSpace(val), `"`)
				}
			}
			if r.kbps == 0 {
				return nil, errors.New("vidstream: master manifest: variant without BANDWIDTH")
			}
			pend = &r
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if pend == nil {
			continue // URI line without a preceding STREAM-INF (not a variant)
		}
		u, err := url.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("vidstream: master manifest: bad variant URI %q: %w", line, err)
		}
		pend.playlist = base.ResolveReference(u)
		out = append(out, *pend)
		pend = nil
	}
	if len(out) == 0 {
		return nil, errors.New("vidstream: master manifest lists no variants")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].kbps < out[j].kbps })
	return out, nil
}

// parseMedia parses one rendition's media playlist into its segment list:
// #EXTINF duration lines, each followed by the segment's URI, resolved
// against base.
func parseMedia(base *url.URL, body string) ([]segmentRef, error) {
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return nil, errors.New("vidstream: media playlist is not an m3u8 playlist")
	}
	var (
		out  []segmentRef
		dur  time.Duration
		have bool
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			numStr, _, _ := strings.Cut(v, ",")
			secs, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
			if err != nil || secs <= 0 {
				return nil, fmt.Errorf("vidstream: media playlist: bad EXTINF %q", v)
			}
			dur = time.Duration(secs * float64(time.Second))
			have = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !have {
			continue // URI line without a preceding EXTINF (not a segment)
		}
		u, err := url.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("vidstream: media playlist: bad segment URI %q: %w", line, err)
		}
		out = append(out, segmentRef{url: base.ResolveReference(u), dur: dur})
		have = false
	}
	if len(out) == 0 {
		return nil, errors.New("vidstream: media playlist lists no segments")
	}
	return out, nil
}

// splitAttrs splits an m3u8 attribute list on commas outside double quotes
// (NAME="a,b" must stay one attribute).
func splitAttrs(s string) []string {
	var (
		out    []string
		start  int
		quoted bool
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
