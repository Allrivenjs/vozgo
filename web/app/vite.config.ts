import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// La UI se compila a web/dist, que el binario de Go embebe con go:embed.
// Todo queda inline o local: la herramienta no debe pedirle nada a internet.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    assetsInlineLimit: 0,
  },
  server: {
    // `npm run dev` habla con un `vozgo serve` local.
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
