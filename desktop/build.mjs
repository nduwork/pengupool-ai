// Bundles the main process and preload with esbuild (CommonJS, Node target, electron external).
import { build } from "esbuild";

const common = { bundle: true, platform: "node", format: "cjs", target: "node22", external: ["electron"], sourcemap: false, logLevel: "warning" };
await build({ ...common, entryPoints: ["src/main.ts"], outfile: "out/main.js" });
await build({ ...common, entryPoints: ["src/preload.ts"], outfile: "out/preload.js" });
console.log("built out/main.js, out/preload.js");
