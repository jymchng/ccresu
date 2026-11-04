import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** Tailwind-aware class merge, as used by shadcn/ui components. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
