import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    // changeOrigin makes the Go server see its own APP_URL host.
    proxy: { "/api": { target: "http://localhost:8080", changeOrigin: true } },
  },
});
