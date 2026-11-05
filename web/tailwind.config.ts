import type { Config } from "tailwindcss";

/**
 * Tailwind v3 configuration.
 *
 * Colours are CSS custom properties (see app/globals.css) so the light and dark
 * palettes are one class swap on <html>, and `darkMode: "class"` lets the
 * pre-paint bootstrap script choose the palette without a flash.
 */
const config: Config = {
  darkMode: "class",
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
    "./lib/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        /*
         * Channel form, not `var(--background)`, because Tailwind v3 can only
         * apply an opacity modifier (`bg-muted/40`, `bg-background/80`) to a
         * colour it can wrap in `rgb(… / <alpha>)`. With a bare `var()` it
         * emits nothing at all: those utilities were silently missing from the
         * stylesheet, so the sticky header had no background, table headers had
         * no tint, and the hero pill had no fill — while every build and test
         * stayed green. `globals.css` keeps `--x-rgb` as the source of truth and
         * derives `--x: rgb(var(--x-rgb))` from it, so nothing is duplicated.
         */
        background: "rgb(var(--background-rgb) / <alpha-value>)",
        foreground: "rgb(var(--foreground-rgb) / <alpha-value>)",
        card: "rgb(var(--card-rgb) / <alpha-value>)",
        "card-foreground": "rgb(var(--card-foreground-rgb) / <alpha-value>)",
        muted: "rgb(var(--muted-rgb) / <alpha-value>)",
        "muted-foreground": "rgb(var(--muted-foreground-rgb) / <alpha-value>)",
        primary: "rgb(var(--primary-rgb) / <alpha-value>)",
        "primary-foreground": "rgb(var(--primary-foreground-rgb) / <alpha-value>)",
        secondary: "rgb(var(--secondary-rgb) / <alpha-value>)",
        "secondary-foreground": "rgb(var(--secondary-foreground-rgb) / <alpha-value>)",
        accent: "rgb(var(--accent-rgb) / <alpha-value>)",
        "accent-foreground": "rgb(var(--accent-foreground-rgb) / <alpha-value>)",
        border: "rgb(var(--border-rgb) / <alpha-value>)",
        ring: "rgb(var(--ring-rgb) / <alpha-value>)",
      },
      borderRadius: {
        lg: "0.75rem",
        md: "0.5rem",
        sm: "0.375rem",
      },
    },
  },
  plugins: [],
};

export default config;
