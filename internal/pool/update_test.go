package pool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// fakeReleases puts a stand-in gh first on PATH: signed in unless $PP_SIGNED_OUT, the release list
// is $PP_GH/releases.json, and a download writes the asset named by -p and logs its args.
func fakeReleases(t *testing.T, version string) (*Pool, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	script := `#!/bin/sh
case "$1 $2" in
"auth status")
  if [ -f "$PP_GH/signed_out" ]; then echo '{"hosts":{"github.com":[]}}'; else echo '{"hosts":{"github.com":[{"login":"me","active":true,"state":"success"}]}}'; fi ;;
"api repos/nduwork/pengupool-ai/releases?per_page=20") cat "$PP_GH/releases.json" ;;
"release download")
  echo "$@" > "$PP_GH/download.args"
  while [ $# -gt 0 ]; do [ "$1" = "-D" ] && dir="$2"; [ "$1" = "-p" ] && asset="$2"; shift; done
  echo dmg > "$dir/$asset" ;;
*) echo "unexpected gh $*" >&2; exit 1 ;;
esac
`
	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + string(filepath.ListSeparator) + os.Getenv("PATH"), "HOME=" + t.TempDir(), "PP_GH=" + root}
	return New(Options{Store: st, Exe: "pengupool", Env: env, Version: version}), root
}

const releasesJSON = `[
 {"tag_name":"v0.8.10-rc.1","draft":false,"prerelease":true,"html_url":"https://github.com/nduwork/pengupool-ai/releases/tag/v0.8.10-rc.1","body":"rc","assets":[{"name":"PenguPool-AI-0.8.10-rc.1-mac-universal.dmg"},{"name":"PenguPool-AI-0.8.10-rc.1-mac-arm64.dmg"},{"name":"notes.txt"}]},
 {"tag_name":"v0.9.0","draft":true,"html_url":"d","body":"draft","assets":[{"name":"PenguPool-AI-0.9.0-mac-universal.dmg"}]},
 {"tag_name":"v0.8.9","draft":false,"prerelease":true,"html_url":"https://github.com/nduwork/pengupool-ai/releases/tag/v0.8.9","body":"  Faster.  ","assets":[{"name":"PenguPool-AI-0.8.9-mac-universal.dmg"}]},
 {"tag_name":"v0.8.11","draft":false,"html_url":"x","body":"no image yet","assets":[]},
 {"tag_name":"v0.8.3","draft":false,"html_url":"y","body":"","assets":[{"name":"PenguPool-AI-0.8.3-mac-universal.dmg"}]},
 {"tag_name":"nightly","draft":false,"html_url":"z","body":"","assets":[{"name":"PenguPool-AI-nightly-mac-universal.dmg"}]}
]`

func TestCompareVersions(t *testing.T) {
	for _, c := range [][2]string{{"v0.8.2", "v0.8.3"}, {"0.8.9", "v0.8.10"}, {"v0.8.10-rc.1", "v0.8.10"}, {"v0.8.10-rc.2", "v0.8.10-rc.10"}, {"v0.8.10-1", "v0.8.10-rc"}, {"v0.8.10-rc", "v0.8.10-rc.1"}, {"v0.9.0", "v1.0.0"}} {
		if compareVersions(c[0], c[1]) >= 0 || compareVersions(c[1], c[0]) <= 0 {
			t.Errorf("want %s < %s", c[0], c[1])
		}
	}
	if compareVersions("v0.8.3", "0.8.3") != 0 {
		t.Error("v0.8.3 and 0.8.3 are the same version")
	}
}

// The newest non-draft release with a disk image wins by semver (a release beats its own
// pre-release, 0.8.10-rc.1 beats 0.8.9); it is published in the domain only when newer.
func TestUpdateCheck(t *testing.T) {
	p, root := fakeReleases(t, "0.8.2")
	os.WriteFile(filepath.Join(root, "releases.json"), []byte(releasesJSON), 0o644)
	ctx := context.Background()
	got, err := p.updateCheck(ctx, refreshIn{})
	if err != nil {
		t.Fatal(err)
	}
	u := got.(protocol.Update)
	if u.Error != "" || !u.Newer || u.Latest != "0.8.10-rc.1" || u.Tag != "v0.8.10-rc.1" || u.Current != "0.8.2" || u.Asset != "PenguPool-AI-0.8.10-rc.1-mac-arm64.dmg" || u.Notes != "rc" {
		t.Fatalf("check %+v", u)
	}
	if d := p.st.State().Domain.Update; d == nil || d.Tag != "v0.8.10-rc.1" {
		t.Fatalf("domain update %+v", d)
	}
	// Cached: a changed list is not seen until refreshed.
	os.WriteFile(filepath.Join(root, "releases.json"), []byte(`[{"tag_name":"v0.8.2","html_url":"u","body":"","assets":[{"name":"PenguPool-AI-0.8.2-mac-universal.dmg"}]}]`), 0o644)
	if got, _ := p.updateCheck(ctx, refreshIn{}); got.(protocol.Update).Tag != "v0.8.10-rc.1" {
		t.Fatalf("not cached: %+v", got)
	}
	got, _ = p.updateCheck(ctx, refreshIn{Refresh: true})
	if u := got.(protocol.Update); u.Newer || u.Latest != "0.8.2" || u.Error != "" {
		t.Fatalf("same version: %+v", u)
	}
	if d := p.st.State().Domain.Update; d != nil {
		t.Fatalf("no newer release, yet the domain has %+v", d)
	}
	// Long notes are cut to 2 KB.
	os.WriteFile(filepath.Join(root, "releases.json"), []byte(`[{"tag_name":"v1.0.0","html_url":"u","body":"`+strings.Repeat("é", 2000)+`","assets":[{"name":"PenguPool-AI-1.0.0-mac-universal.dmg"}]}]`), 0o644)
	got, _ = p.updateCheck(ctx, refreshIn{Refresh: true})
	if n := got.(protocol.Update).Notes; len(n) > 2048+len("…") || !strings.HasSuffix(n, "…") || !strings.HasPrefix(n, "éé") {
		t.Fatalf("notes %d bytes", len(n))
	}
}

func TestUpdateCheckSignedOut(t *testing.T) {
	p, root := fakeReleases(t, "0.8.2")
	os.WriteFile(filepath.Join(root, "signed_out"), nil, 0o644)
	got, err := p.updateCheck(context.Background(), refreshIn{})
	if err != nil {
		t.Fatal(err)
	}
	if u := got.(protocol.Update); u.Error != "sign in to GitHub in Settings to check for updates" || u.Newer {
		t.Fatalf("signed out: %+v", u)
	}
	// Not cached: signing in and checking again works at once.
	os.Remove(filepath.Join(root, "signed_out"))
	os.WriteFile(filepath.Join(root, "releases.json"), []byte(releasesJSON), 0o644)
	if got, _ := p.updateCheck(context.Background(), refreshIn{}); !got.(protocol.Update).Newer {
		t.Fatalf("after sign-in: %+v", got)
	}
}

// The download lands in <store>/updates/<tag>, is checked as a disk image, and only two update
// folders stay.
func TestUpdateDownload(t *testing.T) {
	p, root := fakeReleases(t, "0.8.2")
	ctx := context.Background()
	var checked string
	p.testDisk = func(path string) error { checked = path; return nil }
	if _, err := p.updateDownload(ctx, updateDownloadIn{Tag: "v0.8.3; rm -rf /", Asset: "PenguPool-AI-0.8.3-mac-arm64.dmg"}); err == nil {
		t.Fatal("a bad tag was accepted")
	}
	if _, err := p.updateDownload(ctx, updateDownloadIn{Tag: "v0.8.3", Asset: "../PenguPool-*-mac-arm64.dmg"}); err == nil {
		t.Fatal("a bad asset was accepted")
	}
	updates := filepath.Join(p.st.Dir(), "updates")
	for _, tag := range []string{"v0.8.1", "v0.8.2"} {
		os.MkdirAll(filepath.Join(updates, tag), 0o700)
	}
	got, err := p.updateDownload(ctx, updateDownloadIn{Tag: "v0.8.3", Asset: "PenguPool-AI-0.8.3-mac-arm64.dmg"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(updates, "v0.8.3", "PenguPool-AI-0.8.3-mac-arm64.dmg")
	if got.(map[string]string)["path"] != want || checked != want {
		t.Fatalf("path %v, checked %q", got, checked)
	}
	args, _ := os.ReadFile(filepath.Join(root, "download.args"))
	if s := strings.TrimSpace(string(args)); s != "release download v0.8.3 -R nduwork/pengupool-ai -p PenguPool-AI-0.8.3-mac-arm64.dmg -D "+filepath.Join(updates, "v0.8.3")+" --clobber" {
		t.Fatalf("gh %s", s)
	}
	left, _ := os.ReadDir(updates)
	if len(left) != 2 || left[0].Name() != "v0.8.2" || left[1].Name() != "v0.8.3" {
		t.Fatalf("kept %v", left)
	}
	// Not a disk image: refused and removed.
	p.testDisk = func(string) error { return errors.New("not a disk image") }
	if _, err := p.updateDownload(ctx, updateDownloadIn{Tag: "v0.8.4", Asset: "PenguPool-AI-0.8.4-mac-universal.dmg"}); err == nil {
		t.Fatal("a bad image was accepted")
	}
	if _, err := os.Stat(filepath.Join(updates, "v0.8.4")); err == nil {
		t.Fatal("the bad image stayed")
	}
}
