/** @type {import('tailwindcss').Config} */
export default {
  // The editor components (TinyNode, TinyEdge, InlineOverlay, the inspector)
  // are authored with Tailwind utility classes and expect the host to provide
  // them — same contract as the platform webapp. Scan the installed editor
  // source so those classes make it into the SPA's stylesheet.
  content: [
    './index.html',
    './src/**/*.{vue,ts}',
    './node_modules/@tinysystems/editor/src/**/*.{vue,ts}',
  ],
  darkMode: 'media',
  theme: { extend: {} },
  plugins: [],
}
