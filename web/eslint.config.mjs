import next from "eslint-config-next/core-web-vitals";

/**
 * Flat ESLint configuration.
 *
 * `eslint-config-next` exports a flat config array, so it is spread directly.
 * The site is a static export; build output is excluded so linting a fresh
 * checkout and linting after `npm run build` behave identically.
 */
const config = [
  ...next,
  {
    ignores: ["out/**", ".next/**", "node_modules/**", "coverage/**", "next-env.d.ts"],
  },
  {
    files: ["**/*.ts", "**/*.tsx"],
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/consistent-type-imports": "error",
    },
  },
];

export default config;
