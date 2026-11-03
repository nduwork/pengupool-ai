`app-icon.svg` is the mark: three penguins in the pool on their ice tile (navy `#12233B`, teal `#14B8A6`,
beak orange `#F5A524` for beaks only, ice `#E6F4F3`). It is the in-app brand in both themes (the tile keeps the
navy penguins visible on dark), the browser tab icon, and the app icon: `scripts/make-icon.sh` renders it into
`desktop/build/icon.icns` on Apple's icon grid. `favicon.ico` is it rendered at 16, 32 and 48 px (`rsvg-convert`).
`mascot.svg` is the full-body penguin for empty states and onboarding; it is never used as the mark.
`screenshots/` holds the README and help-page images, each in a `-light` and a `-dark` version: the
banner (PNG) and the welcome, start, map, ask and settings screens (WebP). They are real app components
rendered with made-up demo data; replace them by filename.
`media/` holds the README's two demo loops (GIF, 800×500): starting a team with specialists, and an
ask answered from the Compass. They are animated from real app captures with demo data.
`media/muse-loop-light.png` and `media/muse-loop-dark.png` are the README's illustrated "Muse in the loop"
diagram (1600×700): Muse Code on the Mac, the PenguPool hub, and the paired Muse app.
