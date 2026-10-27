# PenguPool AI. `make install` builds PenguPool AI.app into ~/Applications and links the
# `pengu` CLI into ~/.local/bin; `make uninstall` removes both. `make dist` builds the app
# once as a disk image others install without Go, Node or Xcode, and `make release` publishes
# it. Every target is safe to re-run.

VERSION          := $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' cmd/pengu/main.go)
DIST             := build/PenguPool-AI-$(VERSION)-mac-arm64.dmg

.PHONY: install install-app uninstall dist release app web build test smoke clean

install: install-app

# Build the Apple Silicon (arm64) Electron app with the Go host inside, install it, link the CLI.
install-app:
	scripts/build-app.sh

# Remove the app and the CLI link; the data in ~/.pengupool-ai stays (scripts/uninstall.sh --purge).
uninstall:
	scripts/uninstall.sh

# The prebuilt app as a disk image: open it, drag PenguPool AI.app onto Applications.
dist:
	scripts/build-app.sh --no-install
	rm -rf build/dmg $(DIST) && mkdir -p build/dmg
	ditto "desktop/dist/mac-arm64/PenguPool AI.app" build/dmg/PenguPool\ AI.app
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname PenguPool-AI -srcfolder build/dmg -fs HFS+ -format ULFO -ov $(DIST) >/dev/null
	rm -rf build/dmg
	find build -maxdepth 1 -name 'PenguPool-AI-*-mac-arm64.dmg' ! -name '$(notdir $(DIST))' -delete # the new one replaces older images
	@echo "built $(DIST)"

# Publish the disk image as GitHub release v$(VERSION) of this repository, and delete older releases' images.
release: dist
	gh release create v$(VERSION) $(DIST) --title "PenguPool AI $(VERSION)" --notes "Open the disk image, drag PenguPool AI.app onto Applications, open it. See the README's Install section."
	# Older releases keep their notes and tags; their disk images go, so the repository stores one.
	gh release list --limit 200 --json tagName -q '.[].tagName | select(. != "v$(VERSION)")' | while read t; do \
		gh release view "$$t" --json assets -q '.assets[].name' | grep '\.dmg$$' | while read a; do gh release delete-asset "$$t" "$$a" --yes; done; \
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
