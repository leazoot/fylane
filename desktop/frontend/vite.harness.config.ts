import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Development-only root for the design-fidelity harness (harness/README.md).
export default defineConfig({
  root: "harness",
  plugins: [react()],
});
