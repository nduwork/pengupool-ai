import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Latin subsets only: the bundle is embedded in the daemon, so shipping four unused scripts costs
// more than it buys. No CDN and no runtime font fetch.
import "@fontsource-variable/inter";
import "@fontsource-variable/geist-mono";
import "@xterm/xterm/css/xterm.css";
import "./index.css";

import { App } from "./App";

const root = document.getElementById("root");
if (root === null) throw new Error("#root is missing from index.html");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
