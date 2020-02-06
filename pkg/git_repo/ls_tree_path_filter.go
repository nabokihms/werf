package git_repo

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar"

	"github.com/flant/werf/pkg/true_git"
)

type LsTreePathFilter struct {
	true_git.PathFilter

	IncludePathsRelativeToCurrentTree []string
	ExcludePathsRelativeToCurrentTree []string
}

func NewLsTreePathFilter(basePath string, includePaths, excludePaths []string) *LsTreePathFilter {
	formattedIncludePaths := formatGlobs(includePaths)
	formattedExcludePaths := formatGlobs(excludePaths)

	return &LsTreePathFilter{
		PathFilter: true_git.PathFilter{
			BasePath:     basePath,
			IncludePaths: formattedIncludePaths,
			ExcludePaths: formattedExcludePaths,
		},
		IncludePathsRelativeToCurrentTree: formattedIncludePaths,
		ExcludePathsRelativeToCurrentTree: formattedExcludePaths,
	}
}

func (f *LsTreePathFilter) ShouldNotWalkThroughTree() bool {
	return len(f.IncludePathsRelativeToCurrentTree) == 0 && len(f.ExcludePathsRelativeToCurrentTree) == 0
}

func (f *LsTreePathFilter) CheckEntry(entryName string) (bool, bool) {
	var isValidEntry, shouldGoInsideEntry bool

	for _, includePath := range f.IncludePathsRelativeToCurrentTree {
		includePathParts := strings.Split(includePath, string(os.PathSeparator))
		isMatched, err := doublestar.PathMatch(includePathParts[0], entryName)
		if err != nil {
			panic(err)
		}

		if isMatched {
			isValidEntry = true
			if len(includePathParts) > 1 {
				shouldGoInsideEntry = true
				break
			}
		}
	}

	for _, excludePath := range f.ExcludePathsRelativeToCurrentTree {
		excludePathParts := strings.Split(excludePath, string(os.PathSeparator))
		isMatched, err := doublestar.PathMatch(excludePathParts[0], entryName)
		if err != nil {
			panic(err)
		}

		if isMatched {
			if len(excludePathParts) > 1 {
				isValidEntry = true
				shouldGoInsideEntry = true
			} else {
				isValidEntry = false
				shouldGoInsideEntry = false
				break
			}
		}
	}

	return isValidEntry, shouldGoInsideEntry
}

func (f *LsTreePathFilter) WithoutEntryInPaths(entryName string, ff func() error) error {
	oldIncludePathsRelativeToCurrentTree := f.IncludePathsRelativeToCurrentTree
	oldExcludePathsRelativeToCurrentTree := f.ExcludePathsRelativeToCurrentTree
	f.IncludePathsRelativeToCurrentTree = shiftGlobsArray(entryName, f.IncludePathsRelativeToCurrentTree)
	f.ExcludePathsRelativeToCurrentTree = shiftGlobsArray(entryName, f.ExcludePathsRelativeToCurrentTree)
	err := ff()
	f.IncludePathsRelativeToCurrentTree = oldIncludePathsRelativeToCurrentTree
	f.ExcludePathsRelativeToCurrentTree = oldExcludePathsRelativeToCurrentTree

	return err
}

func shiftGlobsArray(entryName string, globs []string) []string {
	var resGlobs []string

	for _, glob := range globs {
		globParts := strings.Split(glob, string(os.PathSeparator))

		isMatched, err := doublestar.PathMatch(globParts[0], entryName)
		if err != nil {
			panic(err)
		}

		if !isMatched {
			continue
		} else if strings.Contains(globParts[0], "**") {
			resGlobs = append(resGlobs, glob)
		} else if len(globParts) > 1 {
			resGlobs = append(resGlobs, filepath.Join(globParts[1:]...))
		}
	}

	return resGlobs
}

func formatGlobs(globs []string) []string {
	var resGlobs []string

	for _, glob := range globs {
		resGlob := glob
		for _, trim := range []string{
			filepath.Join(string(os.PathSeparator), "**", "*"),
			filepath.Join(string(os.PathSeparator), "**"),
			filepath.Join(string(os.PathSeparator), "*"),
		} {
			resGlob = strings.TrimRight(resGlob, trim)
		}

		if resGlob != "" {
			resGlobs = append(resGlobs, resGlob)
		}
	}

	return resGlobs
}
