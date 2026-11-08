// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

// Update check (opt-in): asks GitHub for the releases of YACM and reports a
// newer one. Nothing else is sent: no data about the user or the sessions,
// only the request itself (with "YACM/<version>" as the user agent).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// updateRepo is where releases are published. A modified version that
// publishes its own releases changes this.
const updateRepo = "icavar/yacm"

// UpdateInfo is the answer of CheckForUpdate.
type UpdateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"` // newest release found
	Newer      bool   `json:"newer"`            // Latest is newer than Current
	URL        string `json:"url,omitempty"`    // its release page
	Prerelease bool   `json:"prerelease,omitempty"`
	Published  string `json:"published,omitempty"`
}

type ghRelease struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Published  string `json:"published_at"`
}

var releasesURL = "https://api.github.com/repos/" + updateRepo + "/releases?per_page=30"

// CheckForUpdate looks for a release newer than this one. Release
// candidates are offered only to someone running a release candidate.
func (a *App) CheckForUpdate() (UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return checkForUpdate(ctx, http.DefaultClient, releasesURL, appVersion)
}

func checkForUpdate(ctx context.Context, c *http.Client, url, current string) (UpdateInfo, error) {
	info := UpdateInfo{Current: current}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", appName+"/"+current)
	resp, err := c.Do(req)
	if err != nil {
		return info, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return info, errors.New("no releases published yet")
	case resp.StatusCode != http.StatusOK:
		return info, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rels []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return info, fmt.Errorf("unexpected answer from GitHub: %w", err)
	}
	cur, ok := parseVersion(current)
	if !ok {
		return info, fmt.Errorf("unknown version %q", current)
	}
	var best *ghRelease
	var bestV version
	for i := range rels {
		r := &rels[i]
		v, ok := parseVersion(r.Tag)
		if r.Draft || !ok || (v.rc > 0 && cur.rc == 0) {
			continue
		}
		if best == nil || v.compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	if best == nil {
		return info, nil
	}
	info.Latest = strings.TrimPrefix(best.Tag, "v")
	// The download link opens in the browser: only YACM's own release pages.
	info.URL = "https://github.com/" + updateRepo + "/releases/tag/" + neturl.PathEscape(best.Tag)
	if strings.HasPrefix(best.URL, "https://github.com/"+updateRepo+"/releases/") {
		info.URL = best.URL
	}
	info.Prerelease = best.Prerelease
	info.Published = best.Published
	info.Newer = bestV.compare(cur) > 0
	return info, nil
}

// version is 0.9.0 or 0.9.0-rc.1 (rc 0: a final release).
type version struct {
	n  [3]int
	rc int
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$`)

func parseVersion(s string) (version, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return version{}, false
	}
	var v version
	for i := 0; i < 3; i++ {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	if m[4] != "" {
		v.rc, _ = strconv.Atoi(m[4])
	}
	return v, true
}

// compare orders versions; a release candidate comes before its release.
func (v version) compare(o version) int {
	for i := range v.n {
		if v.n[i] != o.n[i] {
			if v.n[i] < o.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.rc == o.rc:
		return 0
	case v.rc == 0:
		return 1
	case o.rc == 0:
		return -1
	case v.rc < o.rc:
		return -1
	}
	return 1
}
