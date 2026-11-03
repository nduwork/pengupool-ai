The mark is three penguins in the pool. Navy `#12233B`, teal `#14B8A6`, beak orange `#F5A524` (beaks only), ice `#E6F4F3`.
`logo.svg` is the colour mark, for 32 px and up; `logo-16.svg` is the small cut (the front penguin) below 32 px.
On a dark background use `logo-16-tile.svg` (the small cut on its ice tile): never the colour mark directly.
`favicon.svg` and `favicon.ico` (16/32/48) are the browser tab icon.
`app-icon.svg` is the app icon on its ice tile, `app-icon-small.svg` its 16 and 32 px cut; `scripts/make-icon.sh`
renders both into `desktop/build/icon.icns` on Apple's icon grid.
`mascot.svg` is the full-body penguin for empty states and onboarding; it is never used as the mark.
`screenshots/` holds the README and help-page images, each in a `-light` and a `-dark` version: the
banner (PNG) and the welcome, start, map, ask and settings screens (WebP). They are real app components
rendered with made-up demo data; replace them by filename.
`media/` holds the README's two demo loops (GIF, 800×500): starting a team with specialists, and an
ask answered from the Compass. They are animated from real app captures with demo data.
`media/muse-loop-light.png` and `media/muse-loop-dark.png` are the README's illustrated "Muse in the loop"
diagram (1600×700): Muse Code on the Mac, the PenguPool hub, and the paired Muse app.
