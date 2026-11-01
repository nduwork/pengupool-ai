import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The theme's own font sizes (index.css --text-*): registered so tailwind-merge reads text-label as a
// size, not a colour that a later text-danger would replace.
const twMerge = extendTailwindMerge({ extend: { theme: { text: ["micro", "caption", "label", "body", "title"] } } });

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

/** A file's modification time as `ls --time-style=long-iso` prints it: `2026-10-20 14:05`, local time. */
export function shortTime(iso: string | undefined): string {
  if (iso === undefined || iso === "") return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** The full local date and time, for a tooltip. */
export function fullTime(iso: string | undefined): string {
  if (iso === undefined || iso === "") return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}
