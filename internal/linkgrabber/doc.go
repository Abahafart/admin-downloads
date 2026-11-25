// Package linkgrabber extracts URLs from free text (clipboard paragraphs,
// stdin, pasted lists), matches them against the hosters.Registry (old and
// new URL formats are each plugin's job), deduplicates them against the
// queue and history, and expands folders into trees (PLAN.md 9.2 and 9.3).
// The clipboard text is processed in memory only and never logged.
//
// TODO(linkgrabber agent): implement.
package linkgrabber
