// Package workloadpolicy is the trust policy matcher of workload identity
// federation (ADR 0019, "Matcher language"). A policy is an AND list of
// {claim, op, values} conditions over the claims of a verified workload token.
//
// The language is deliberately small: no regular expressions and no embedded
// interpreter. Operators are eq, in, glob (a single star stays inside a path
// segment, a double star crosses slashes, anchored) and prefix. Comparisons are
// exact and case-sensitive on string, boolean and integer claims; a missing claim
// fails its condition. Validation on write enforces, per issuer kind, the
// immutable-identity conditions that prevent confused-deputy and name-squatting
// attacks, and refuses globs that would match every subject.
package workloadpolicy
