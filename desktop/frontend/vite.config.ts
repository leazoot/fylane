import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  // The theme test reads style.css as a string to check the two dark blocks
  // agree. Vitest blanks CSS imports by default, which blanks `?raw` too.
  test: {css: true},
})
