// Package fuzzy implements fzf-v1-style subsequence scoring for the command
// palette, quick open (file paths) and the project picker (specs 04, 05, 07).
//
// Matching is case-insensitive; a case-sensitive exact character earns a small
// bonus. Matches score higher at the start of the string, after a separator
// (space, / _ - .), at camelCase and letter/digit boundaries, and in
// consecutive runs; gaps between matches cost a penalty. For paths, matches in
// the basename (after the last '/') earn an extra bonus.
//
// Rank is the bulk entry point. It allocates nothing per non-matching
// candidate and keeps only the best `limit` matches in a bounded heap.
package fuzzy
