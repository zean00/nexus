import { rm } from "node:fs/promises";
import * as esbuild from "esbuild";

await rm("dist", { recursive: true, force: true });

await esbuild.build({
  entryPoints: ["src/embedded.tsx"],
  outfile: "dist/app.js",
  bundle: true,
  format: "esm",
  sourcemap: false,
  minify: false,
  jsx: "automatic",
  loader: {
    ".ts": "ts",
    ".tsx": "tsx",
    ".css": "css"
  },
  target: ["es2020"],
  logLevel: "info",
  legalComments: "none",
  define: {
    "process.env.NODE_ENV": "\"production\""
  }
});
