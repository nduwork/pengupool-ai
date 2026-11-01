`logo.svg` is the mark (in-app brand, any size above 24 px); `logo-16.svg` is its 16 px drawing (web favicon).
`app-icon.svg` is the full-bleed 1024 px app icon; `scripts/make-icon.sh` renders it into `desktop/build/icon.icns`.
`logo-mono.svg` is the one-colour mark (`currentColor`) for menu-bar or print use; nothing loads it yet.
`mascot.svg` is the full-body penguin for empty states and onboarding; it is never used as the mark.
`screenshots/` holds the README and help-page images, each in a `-light` and a `-dark` version: the
banner (PNG) and the welcome, start, map, ask and settings screens (WebP). They are real app components
rendered with made-up demo data; replace them by filename.
`media/` holds the README's two demo loops (GIF, 800×500): starting a team with specialists, and an
ask answered from the Compass. They are animated from real app captures with demo data.
`media/muse-loop-light.png` and `media/muse-loop-dark.png` are the README's illustrated "Muse in the loop"
diagram (1600×700): Muse Code on the Mac, the PenguPool hub, and the paired Muse app.
