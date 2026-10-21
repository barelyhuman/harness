import { defineConfig } from "vite";
import preact from "@preact/preset-vite";
import tailwindcss from "@tailwindcss/vite";
import { writeFileSync } from "node:fs";
import wails from "@wailsio/runtime/plugins/vite";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  worker: { format: "es" },
  plugins: [
    preact(),
    tailwindcss(),
    wails("./src/lib/bindings"),
    {
      // Keep dist/.gitkeep so Go's //go:embed all:frontend/dist compiles on a fresh checkout.
      name: "keep-dist-gitkeep",
      closeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
});
