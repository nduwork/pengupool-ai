package pool

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// In-app update. PenguPool's releases are GitHub releases of its own (private) repository, so the
// host reaches them through gh with the owner's account: update.check finds the newest one,
// update.download puts its disk image under <store>/updates/<tag>, and the desktop app installs it
// and restarts (desktop/src/update.ts). Agent sessions keep running: workers outlive the host.

const (
	releaseRepo = "nduwork/pengupool-ai"
	updatesDir  = "updates"
)

var (
	releaseTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	// A release's disk image: Apple Silicon only from 0.8.3, universal before. arm64 wins when a
	// release has both.
	releaseAssetRe = regexp.MustCompile(`^PenguPool-AI-[0-9A-Za-z.-]+-mac-(arm64|universal)\.dmg$`)
)

// releaseImage is the disk image to install among a release's assets, or "".
func releaseImage(names []string) string {
	pick := ""
	for _, n := range names {
		if releaseAssetRe.MatchString(n) && (pick == "" || strings.HasSuffix(n, "-mac-arm64.dmg")) {
			pick = n
		}
	}
	return pick
}

// updateCheck reports the newest non-draft release with a disk image (pre-releases count) and
// whether it is newer than this host; a successful answer is kept for 30 minutes and published in
// the domain, so the title bar shows it without asking. A newer release with no image this app can
// install (one still being published) is named in Error, so About never calls this the newest.
func (p *Pool) updateCheck(ctx context.Context, in refreshIn) (any, error) {
	p.updMu.Lock()
	defer p.updMu.Unlock()
	if p.upd != nil && !in.Refresh && time.Since(p.updAt) < 30*time.Minute {
		return *p.upd, nil
	}
	u := protocol.Update{Current: p.version}
	switch p.githubStatus()["state"] {
	case "signed_in":
	case "no_cli":
		u.Error = "install the GitHub CLI (brew install gh), then sign in to GitHub in Settings to check for updates"
		return u, nil
	default:
		u.Error = "sign in to GitHub in Settings to check for updates"
		return u, nil
	}
	raw, err := p.ghRunFor(ctx, 20*time.Second, "", "api", "repos/"+releaseRepo+"/releases?per_page=20")
	if err != nil {
		u.Error = "Could not reach GitHub to check for updates (" + err.Error() + ")"
		return u, nil // not cached: the next check asks again
	}
	var rels []struct {
		Tag    string `json:"tag_name"`
		Draft  bool   `json:"draft"`
		URL    string `json:"html_url"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(raw), &rels); err != nil {
		u.Error = "unexpected answer from GitHub: " + err.Error()
		return u, nil
	}
	unusable, unusableURL := "", "" // the newest release with no image this app can install
	for _, r := range rels {
		if r.Draft || !releaseTagRe.MatchString(r.Tag) {
			continue
		}
		var names []string
		for _, a := range r.Assets {
			names = append(names, a.Name)
		}
		img := releaseImage(names)
		if img == "" {
			if unusable == "" || compareVersions(r.Tag, unusable) > 0 {
				unusable, unusableURL = r.Tag, r.URL
			}
			continue
		}
		if u.Tag == "" || compareVersions(r.Tag, u.Tag) > 0 {
			u.Tag, u.Latest, u.URL, u.Asset, u.Notes = r.Tag, strings.TrimPrefix(r.Tag, "v"), r.URL, img, trimNotes(r.Body)
		}
	}
	u.Newer = u.Tag != "" && compareVersions(u.Tag, p.version) > 0
	if unusable != "" && compareVersions(unusable, p.version) > 0 && (u.Tag == "" || compareVersions(unusable, u.Tag) > 0) {
		u.Error = "PenguPool " + strings.TrimPrefix(unusable, "v") + " is out, but its release has no disk image this app can install yet (" + unusableURL + "). Try again in a few minutes, or download it from the release page."
	}
	p.upd, p.updAt = &u, time.Now()
	if u.Newer {
		p.publishUpdate(&u)
	} else {
		p.publishUpdate(nil)
	}
	return u, nil
}

// trimNotes keeps the first 2 KB of release notes, on a character boundary.
func trimNotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 2048 {
		return s
	}
	return strings.ToValidUTF8(s[:2048], "") + "…"
}

func (p *Pool) publishUpdate(u *protocol.Update) {
	if reflect.DeepEqual(u, p.st.State().Domain.Update) {
		return
	}
	_ = p.update(func(st *store.State) error {
		st.Domain.Update = u
		return nil
	})
}

// updateLoop checks a minute after start, then every 6 hours.
func (p *Pool) updateLoop(ctx context.Context) {
	if p.version == "" {
		return
	}
	// A notice the previous version saved is stale now, and so is any download it did not install.
	if u := p.st.State().Domain.Update; u != nil && u.Current != p.version {
		p.publishUpdate(nil)
	}
	pruneInstalled(filepath.Join(p.st.Dir(), updatesDir), p.version)
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_, _ = p.updateCheck(ctx, refreshIn{Refresh: true})
		wait = 6 * time.Hour
	}
}

type updateDownloadIn struct {
	Tag   string `json:"tag"`
	Asset string `json:"asset"` // update.check's asset
}

// updateDownload fetches a release's disk image into <store>/updates/<tag> and checks it is a disk
// image; nothing else is kept under updates/.
func (p *Pool) updateDownload(ctx context.Context, in updateDownloadIn) (any, error) {
	if !releaseTagRe.MatchString(in.Tag) {
		return nil, invalid("not a release tag: %q", in.Tag)
	}
	if !releaseAssetRe.MatchString(in.Asset) {
		return nil, invalid("not a PenguPool disk image: %q", in.Asset)
	}
	if !p.dlMu.TryLock() {
		return nil, conflict("an update is already downloading")
	}
	defer p.dlMu.Unlock()
	root := filepath.Join(p.st.Dir(), updatesDir)
	dir := filepath.Join(root, in.Tag)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := p.ghRunFor(ctx, 15*time.Minute, "", "release", "download", in.Tag, "-R", releaseRepo, "-p", in.Asset, "-D", dir, "--clobber"); err != nil {
		return nil, err
	}
	img := filepath.Join(dir, in.Asset)
	if _, err := os.Stat(img); err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "release %s: %s was not downloaded", in.Tag, in.Asset)
	}
	check := p.testDisk
	if check == nil {
		check = func(path string) error {
			if out, err := exec.CommandContext(ctx, "hdiutil", "imageinfo", path).CombinedOutput(); err != nil {
				return protocol.Errorf(protocol.CodeUnavailable, "the download is not a disk image: %s", firstNonEmpty(strings.TrimSpace(string(out)), err.Error()))
			}
			return nil
		}
	}
	if err := check(img); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	_ = os.Chmod(img, 0o600) // gh writes it with the umask
	pruneUpdates(root, in.Tag)
	return map[string]string{"path": img}, nil
}

// pruneUpdates removes every entry of updates/ but keep, whatever its name. RemoveAll unlinks a
// symlink entry and never follows it.
func pruneUpdates(root, keep string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != keep {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// pruneInstalled removes, at start, what an update left: every entry of updates/ that is not a
// release newer than the running version. An owner who installs by dragging the disk image never
// runs the app's install script, so this is the only clean-up then. A still-newer download stays.
func pruneInstalled(root, version string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !releaseTagRe.MatchString(e.Name()) || compareVersions(e.Name(), version) <= 0 {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// compareVersions orders semantic versions (a leading v is ignored): 0.8.3-rc.1 < 0.8.3 < 0.8.10.
// Pre-release identifiers compare numerically when both are numbers, else as text.
func compareVersions(a, b string) int {
	coreA, preA, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	coreB, preB, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if c := compareIdents(strings.Split(coreA, "."), strings.Split(coreB, "."), true); c != 0 {
		return c
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return compareIdents(strings.Split(preA, "."), strings.Split(preB, "."), false)
}

func compareIdents(a, b []string, numeric bool) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) {
			return -1
		}
		if i >= len(b) {
			return 1
		}
		x, errX := strconv.Atoi(a[i])
		y, errY := strconv.Atoi(b[i])
		switch {
		case errX == nil && errY == nil:
			if x != y {
				return cmp.Compare(x, y)
			}
		case numeric || errX == nil: // a number sorts before text in a pre-release
			return -1
		case errY == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return 0
}
