# PenguPool Muse. `make dist` builds the disk image others install without git;
# `make release` publishes it as this repository's GitHub release. Every target
# is safe to re-run.

VERSION          := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
DIST             := build/PenguPool-Muse-$(VERSION)-mac-arm64.dmg

.PHONY: dist release smoke-dist clean

# The prebuilt tool as a disk image: open it, double-click Install.command.
dist:
	rm -rf build/dmg $(DIST) && mkdir -p build/dmg/pengupool-muse
	ditto pengu build/dmg/pengupool-muse/pengu
	mkdir -p build/dmg/pengupool-muse/experiment
	cp experiment/tasks.json build/dmg/pengupool-muse/experiment/tasks.json
	cp README.md DESIGN.md LICENSE build/dmg/pengupool-muse/
	cp scripts/Install.command scripts/Uninstall.command build/dmg/
	hdiutil create -volname PenguPool-Muse -srcfolder build/dmg -fs HFS+ -format ULFO -ov $(DIST) >/dev/null
	rm -rf build/dmg
	find build -maxdepth 1 -name 'PenguPool-Muse-*-mac-arm64.dmg' ! -name '$(notdir $(DIST))' -delete # the new one replaces older images
	@echo "built $(DIST)"

# Publish the disk image as GitHub release v$(VERSION), and delete older releases' images.
release: dist
	gh release create v$(VERSION) $(DIST) --title "PenguPool Muse $(VERSION)" --notes "Open the disk image, double-click Install.command, then run: pengu list. See the README's Install section."
	# Older releases keep their notes and tags; their disk images go, so the repository stores one.
	gh release list --limit 200 --json tagName -q '.[].tagName | select(. != "v$(VERSION)")' | while read t; do \
		gh release view "$$t" --json assets -q '.assets[].name' | grep '\.dmg$$' | while read a; do gh release delete-asset "$$t" "$$a" --yes; done; \
	done

# Mount the image, install into a throwaway HOME, and run the tool.
smoke-dist: dist
	TMPHOME=$$(mktemp -d) && \
	hdiutil attach $(DIST) -mountpoint $$TMPHOME/mnt >/dev/null && \
	HOME=$$TMPHOME sh $$TMPHOME/mnt/Install.command >/dev/null && \
	HOME=$$TMPHOME $$TMPHOME/.local/bin/pengu list; \
	rc=$$?; hdiutil detach $$TMPHOME/mnt >/dev/null; rm -rf $$TMPHOME; exit $$rc

clean:
	rm -rf build
