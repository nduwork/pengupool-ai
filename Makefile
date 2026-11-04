# PenguPool AI. `make install` builds PenguPool AI.app into /Applications (or ~/Applications) and links the
# `pengupool-ai` (host) CLI into ~/.local/bin; `make uninstall` removes them.
# `make dist` builds the app once as a disk image others install without
# Go, Node or Xcode, and `make release` publishes it. Every target is safe to re-run.

# Only a plain version (1.2.3, 1.2.3-rc.1) is read; anything else leaves VERSION empty and dist/publish refuse.
VERSION          := $(shell sed -nE 's/^const Version = "([0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.]+)?)"$$/\1/p' cmd/pengupool/main.go)
DIST             := build/PenguPool-AI-$(VERSION)-mac-arm64.dmg

.PHONY: install install-app uninstall dist release publish app web build test smoke clean

install: install-app

# Build the Apple Silicon (arm64) Electron app with the Go host inside, install it, link the CLI.
install-app:
	scripts/build-app.sh

# Remove the app and the CLI link; the data in ~/.pengupool-ai stays (scripts/uninstall.sh --purge).
uninstall:
	scripts/uninstall.sh

# The prebuilt app as a disk image: open it, drag PenguPool AI.app onto Applications. With a Developer ID
# identity in the keychain the app and the image are signed, notarized and stapled (scripts/notarize.sh).
dist:
	@test -n "$(VERSION)" || { echo "no valid Version in cmd/pengupool/main.go" >&2; exit 1; }
	SIGN=1 scripts/build-app.sh --no-install
	scripts/notarize.sh "desktop/dist/mac-arm64/PenguPool AI.app"
	rm -rf build/dmg $(DIST) && mkdir -p build/dmg
	ditto "desktop/dist/mac-arm64/PenguPool AI.app" build/dmg/PenguPool\ AI.app
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname PenguPool-AI -srcfolder build/dmg -fs HFS+ -format ULFO -ov $(DIST) >/dev/null
	rm -rf build/dmg
	scripts/notarize.sh $(DIST) "desktop/dist/mac-arm64/PenguPool AI.app" || { rm -f $(DIST); exit 1; } # no un-notarized image left behind
	find build -maxdepth 1 -name 'PenguPool-AI-*-mac-arm64.dmg' ! -name '$(notdir $(DIST))' -delete # the new one replaces older images
	@echo "built $(DIST)"

# Build the disk image and publish it as GitHub release v$(VERSION) of this repository.
release: dist
	$(MAKE) publish

# Attach the built disk image to release v$(VERSION), made at this commit when it does not exist yet.
# Re-running replaces the image.
publish:
	@test -n "$(VERSION)" || { echo "no valid Version in cmd/pengupool/main.go" >&2; exit 1; }
	test -f $(DIST) || { echo "no $(DIST): run make dist first" >&2; exit 1; }
	gh release view v$(VERSION) >/dev/null 2>&1 || gh release create v$(VERSION) --target $$(git rev-parse HEAD) --title "PenguPool AI $(VERSION)" --notes "Open the disk image, drag PenguPool AI.app onto Applications, open it. See the README's Install section."
	gh release upload v$(VERSION) $(DIST) --clobber
	# Older releases keep their notes and tags; their disk images go, so the repository stores one. Only when this
	# release is the latest (never a prerelease or a backport), and only when gh can say so: otherwise none go.
	latest=$$(gh release view --json tagName -q .tagName) && [ "$$latest" = v$(VERSION) ] || { echo "v$(VERSION) is not the latest release: older disk images kept"; exit 0; }; \
	gh release list --limit 200 --json tagName -q '.[].tagName | select(. != "v$(VERSION)")' | while read -r t; do \
		gh release view "$$t" --json assets -q '.assets[].name' | grep '\.dmg$$' | while read -r a; do gh release delete-asset "$$t" "$$a" --yes; done; \
	done

# Build PenguPool AI.app into desktop/dist/mac-arm64 without installing it.
app:
	scripts/build-app.sh --no-install

# The browser app, into internal/webapp/dist.
web:
	cd web && { [ -d node_modules ] || npm ci; } && npm run build

build:
	go build ./...

test:
	go test ./... -race

smoke:
	scripts/smoke.sh

clean:
	rm -rf build desktop/dist desktop/out
