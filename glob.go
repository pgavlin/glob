// Package glob provides utilities for matching patterns against filesystem trees.
//
// The matcher is designed to read as few directories as possible while matching; only directories that may contain
// matches are considered.
package glob

import (
	"errors"
	"io/fs"
	"iter"
	"path"
	"slices"
	"strings"

	"github.com/pgavlin/fx/v2"
)

func match(pattern, name string) bool {
	ok, _ := path.Match(pattern, name)
	return ok
}

// A pattern represents a single glob pattern.
//
// The first entry in the pattern represents the pattern to apply to each entry in the current directory; the rest of
// the entries apply to child directories.
type pattern []string

func (p pattern) String() string {
	return path.Join(p...)
}

// newPattern creates a new pattern from the given string.
func newPattern(p string, patterns *[]pattern) error {
	// Validate the pattern. Note that '**' is a valid path pattern, so we don't need to check for it explicitly.
	_, err := path.Match(p, "")
	if err != nil {
		return err
	}

	// Split the pattern into its consituent elements and strip out any empty patterns.
	steps := slices.Collect(fx.Filter(strings.SplitSeq(p, "/"), func(s string) bool { return s != "" }))
	if len(steps) == 0 {
		steps = []string{""}
	}

	// Append the pattern. If the pattern starts with "**", also append its advancement. This allows "**/foo" to match "foo" in the root directory.
	*patterns = append(*patterns, pattern(steps))
	if steps[0] == "**" && len(steps) != 1 {
		*patterns = append(*patterns, pattern(steps[1:]))
	}
	return nil
}

// newPatterns is a convenience function to create a list of patterns from a list of strings.
func newPatterns(ps []string) ([]pattern, error) {
	var patterns []pattern
	var errs []error
	for _, i := range ps {
		if err := newPattern(i, &patterns); err != nil {
			errs = append(errs, err)
		}
	}
	return patterns, errors.Join(errs...)
}

// matchDir attempts to match p against the given directory name.
//
// If the current step matches and there are more steps in the pattern, match appends the rest of the pattern to patterns.
func (p pattern) matchDir(name string, patterns *[]pattern) bool {
	step, rest := p[0], p[1:]
	if step == "**" {
		// If the current step is "**", we always continue matching the pattern.
		*patterns = append(*patterns, p)
	} else if !match(step, name) {
		// If the pattern does not match, we're done.
		return false
	}
	// If there are no more steps in the pattern, we have a match.
	if len(rest) == 0 {
		return true
	}

	// Otherwise, continue matching.
	*patterns = append(*patterns, rest)
	return false
}

// matchFile attempts to match p against the given filename.
func (p pattern) matchFile(name string) bool {
	return len(p) == 1 && (p[0] == "**" || match(p[0], name))
}

func always(patterns []pattern) bool {
	for _, p := range patterns {
		if len(p) == 1 && p[0] == "**" {
			return true
		}
	}
	return false
}

// hasMeta reports whether p contains any of the metacharacters recognized by path.Match.
func hasMeta(p string) bool {
	return strings.ContainsAny(p, "*?[\\")
}

func literal(patterns []pattern) (string, []pattern, bool) {
	if len(patterns) != 1 {
		return "", nil, false
	}

	p := patterns[0]
	if hasMeta(p[0]) {
		return "", nil, false
	}

	var next []pattern
	if len(p) > 1 {
		next = []pattern{p[1:]}
	}
	return p[0], next, true
}

// A matchGlob is a glob formed by a list of patterns to include and a list of patterns to exclude.
type matchGlob struct {
	include []pattern
	exclude []pattern
}

func (g *matchGlob) Match(fsys fs.FS, dir string, includeDirs bool) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		matchStep(fsys, dir, false, includeDirs, g.include, g.exclude, yield)
	}
}

func (g *matchGlob) MatchPath(p string) bool {
	names := slices.Collect(fx.Filter(strings.SplitSeq(p, "/"), func(s string) bool { return s != "" }))
	if len(names) == 0 {
		return false
	}

	include, exclude := g.include, g.exclude
	for _, dir := range names[:len(names)-1] {
		var nextInclude, nextExclude []pattern
		for _, p := range exclude {
			if p.matchDir(dir, &nextExclude) {
				return false
			}
		}
		for _, p := range include {
			p.matchDir(dir, &nextInclude)
		}
		if len(nextInclude) == 0 {
			return false
		}
		include, exclude = nextInclude, nextExclude
	}

	var nextInclude, nextExclude []pattern
	last := names[len(names)-1]
	for _, p := range exclude {
		if p.matchDir(last, &nextExclude) {
			return false
		}
	}
	for _, p := range include {
		if p.matchDir(last, &nextInclude) {
			return true
		}
	}
	return false
}

// matchStep advances the current matches against the contents of dir.
func matchStep(fsys fs.FS, dir string, yieldDir, includeDirs bool, include, exclude []pattern, yield func(string, error) bool) bool {
	var nextInclude, nextExclude []pattern

	if always(include) {
		if len(exclude) == 0 {
			return allStep(fsys, dir, yieldDir, includeDirs, yield)
		}
		include = []pattern{{"**"}}
	} else if name, nextInclude, ok := literal(include); ok {
		for _, p := range exclude {
			if p.matchFile(name) {
				return true
			}
		}

		info, err := fs.Stat(fsys, path.Join(dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return true
			}
			return yield(dir, err)
		}
		if info.IsDir() {
			for _, p := range exclude {
				p.matchDir(name, &nextExclude)
			}
			if len(nextInclude) != 0 && !always(nextExclude) {
				return matchStep(fsys, path.Join(dir, name), includeDirs, includeDirs, nextInclude, nextExclude, yield)
			}
			if !includeDirs {
				return true
			}
		}
		return yield(path.Join(dir, name), nil)
	}

	infos, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return yield(dir, err)
	}
	if yieldDir && includeDirs && !yield(dir, nil) {
		return false
	}

match:
	for _, i := range infos {
		nextInclude, nextExclude = nextInclude[:0], nextExclude[:0]

		var included bool
		if !i.IsDir() {
			for _, p := range exclude {
				if p.matchFile(i.Name()) {
					continue match
				}
			}
			for _, p := range include {
				if p.matchFile(i.Name()) {
					included = true
					break
				}
			}
		} else {
			for _, p := range exclude {
				if p.matchDir(i.Name(), &nextExclude) {
					continue match
				}
			}
			for _, p := range include {
				if p.matchDir(i.Name(), &nextInclude) {
					included = includeDirs
				}
			}

			if len(nextInclude) != 0 && !always(nextExclude) {
				// If there is more to do, the caller will yield the matched directory.
				if !matchStep(fsys, path.Join(dir, i.Name()), included, includeDirs, nextInclude, nextExclude, yield) {
					return false
				}
				included = false
			}
		}
		if included && !yield(path.Join(dir, i.Name()), nil) {
			return false
		}
	}
	return true
}

type allGlob struct{}

func (allGlob) Match(fsys fs.FS, dir string, includeDirs bool) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		allStep(fsys, dir, false, includeDirs, yield)
	}
}

func (allGlob) MatchPath(p string) bool {
	return true
}

func allStep(fsys fs.FS, dir string, yieldDir, includeDirs bool, yield func(string, error) bool) bool {
	infos, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return yield(dir, err)
	}
	if yieldDir && includeDirs && !yield(dir, nil) {
		return false
	}

	for _, i := range infos {
		if i.IsDir() {
			if !allStep(fsys, path.Join(dir, i.Name()), true, includeDirs, yield) {
				return false
			}
		} else if !yield(path.Join(dir, i.Name()), nil) {
			return false
		}
	}
	return true
}

type noneGlob struct{}

func (noneGlob) Match(fsys fs.FS, dir string, includeDirs bool) iter.Seq2[string, error] {
	return func(_ func(string, error) bool) {}
}

func (noneGlob) MatchPath(p string) bool {
	return false
}

// A Glob matches paths in a directory against a set of include and exclude patterns.
type Glob interface {
	// Match returns a sequence of (string, error) pairs for paths under dir that match the glob's include and exclude
	// patterns. The error portion of a pair is only non-nil when the path portion is a directory and Match fails to
	// read the directory's entries. If includeDirs is true, matching directories will be included in the sequence prior
	// to their contents.
	Match(fsys fs.FS, dir string, includeDirs bool) iter.Seq2[string, error]

	// MatchPath returns true if the given path matches the glob's includes and excludes.
	MatchPath(path string) bool
}

// New creates a new Glob from the given lists of include and exclude patterns.
//
// A Glob matches a particular path p if any of its include patterns matches p and none of its exclude patterns match p.
//
// The pattern syntax is:
//
//	pattern:
//		pathTerm { '/' pathTerm }
//
//	pathTerm:
//		'**'        matches any sequence of directory names, including the empty sequence
//		{ term }    matches a sequence of terms against a name
//
//	term:
//		'*'         matches any sequence of non-/ characters
//		'?'         matches any single non-/ character
//		'[' [ '^' ] { character-range } ']'
//		            character class (must be non-empty)
//		c           matches character c (c != '*', '?', '\\', '[')
//		'\\' c      matches character c
//
//	character-range:
//		c           matches character c (c != '\\', '-', ']')
//		'\\' c      matches character c
//		lo '-' hi   matches character c for lo <= c <= hi
//
// Patterns require that path terms match all of name, not just a substring. If any error is returned, it will be a list
// of path.ErrBadPattern errors.
func New(includes, excludes []string) (Glob, error) {
	if len(excludes) == 0 && slices.Contains(includes, "**") {
		return allGlob{}, nil
	}
	if len(includes) == 0 || slices.Contains(excludes, "**") {
		return noneGlob{}, nil
	}

	includePatterns, inclErr := newPatterns(includes)
	excludePatterns, exclErr := newPatterns(excludes)
	if err := errors.Join(inclErr, exclErr); err != nil {
		return nil, err
	}
	return &matchGlob{include: includePatterns, exclude: excludePatterns}, nil
}
