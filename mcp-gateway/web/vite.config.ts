import { resolve } from "node:path";
import { defineConfig } from "vite";

const webRoot = resolve(import.meta.dirname);

export default defineConfig({
  root: webRoot,
  base: "/assets/",
  build: {
    outDir: resolve(webRoot, "../internal/api/static"),
    emptyOutDir: true,
    assetsDir: ".",
    cssCodeSplit: false,
    sourcemap: false,
    target: "es2023",
    rollupOptions: {
      input: resolve(webRoot, "index.html"),
      output: {
        entryFileNames: "app.js",
        chunkFileNames: "forbidden-[name].js",
        assetFileNames: "app.[ext]",
        codeSplitting: false,
      },
    },
  },
});
