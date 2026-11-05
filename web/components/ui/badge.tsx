import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-medium transition-colors",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary/10 text-primary",
        /*
         * The outline pill is a *label*, not decoration, so it carries the
         * foreground colour and its own fill. As `text-muted-foreground` on a
         * transparent background it measured 5.95:1 at 12px in the light theme
         * — technically AA, but a hairline pill of grey text on the page wash
         * reads as washed out, which is exactly how it was reported.
         */
        outline: "border-border bg-background/70 text-foreground backdrop-blur-sm",
        success: "border-transparent bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
        warning: "border-transparent bg-amber-500/10 text-amber-700 dark:text-amber-400",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

export function Badge({
  className,
  variant,
  ...props
}: ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { badgeVariants };
