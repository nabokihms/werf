package git_repo

import (
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/bmatcuk/doublestar"
	"gopkg.in/src-d/go-billy.v4/osfs"
	"gopkg.in/src-d/go-git.v4/plumbing/cache"
	"gopkg.in/src-d/go-git.v4/plumbing/filemode"
	"gopkg.in/src-d/go-git.v4/storage/filesystem"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/flant/logboek"
	"github.com/flant/werf/pkg/true_git"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/plumbing/storer"
)

var (
	errNotABranch = errors.New("cannot get branch name: HEAD refers to a specific revision that is not associated with a branch name")
)

type Base struct {
	Name   string
	TmpDir string
}

func (repo *Base) HeadCommit() (string, error) {
	panic("not implemented")
}

func (repo *Base) LatestBranchCommit(branch string) (string, error) {
	panic("not implemented")
}

func (repo *Base) TagCommit(branch string) (string, error) {
	panic("not implemented")
}

func (repo *Base) remoteOriginUrl(repoPath string) (string, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	cfg, err := repository.Config()
	if err != nil {
		return "", fmt.Errorf("cannot access repo config: %s", err)
	}

	if originCfg, hasKey := cfg.Remotes["origin"]; hasKey {
		return originCfg.URLs[0], nil
	}

	return "", nil
}

func (repo *Base) findCommitIdByMessage(repoPath string, regex string, headCommit string) (string, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	headHash, err := newHash(headCommit)
	if err != nil {
		return "", fmt.Errorf("bad head commit hash `%s`: %s", headCommit, err)
	}

	commitObj, err := repository.CommitObject(headHash)
	if err != nil {
		return "", fmt.Errorf("cannot find head commit %s: %s", headCommit, err)
	}

	commitIter := object.NewCommitIterBSF(commitObj, nil, nil)

	regexObj, err := regexp.Compile(regex)
	if err != nil {
		return "", fmt.Errorf("bad regex `%s`: %s", regex, err)
	}

	var foundCommit *object.Commit

	err = commitIter.ForEach(func(c *object.Commit) error {
		if c != nil && regexObj.Match([]byte(c.Message)) {
			foundCommit = c
			return storer.ErrStop
		}

		return nil
	})

	if err != nil && err != plumbing.ErrObjectNotFound {
		return "", fmt.Errorf("failed to traverse repository: %s", err)
	}

	if foundCommit != nil {
		return foundCommit.Hash.String(), nil
	}

	return "", nil
}

func (repo *Base) isEmpty(repoPath string) (bool, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	commitIter, err := repository.CommitObjects()
	if err != nil {
		return false, err
	}

	_, err = commitIter.Next()
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func (repo *Base) getReferenceForRepo(repoPath string) (*plumbing.Reference, error) {
	var err error

	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	return repository.Head()
}

func (repo *Base) getHeadBranchName(repoPath string) (string, error) {
	ref, err := repo.getReferenceForRepo(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot get repo `%s` head: %s", repoPath, err)
	}

	if ref.Name().IsBranch() {
		branchRef := ref.Name()
		return strings.Split(string(branchRef), "refs/heads/")[1], nil
	}

	return "", errNotABranch
}

func (repo *Base) String() string {
	return repo.GetName()
}

func (repo *Base) GetName() string {
	return repo.Name
}

func (repo *Base) createPatch(repoPath, gitDir, workTreeCacheDir string, opts PatchOptions) (Patch, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	fromHash, err := newHash(opts.FromCommit)
	if err != nil {
		return nil, fmt.Errorf("bad `from` commit hash `%s`: %s", opts.FromCommit, err)
	}

	_, err = repository.CommitObject(fromHash)
	if err != nil {
		return nil, fmt.Errorf("bad `from` commit `%s`: %s", opts.FromCommit, err)
	}

	toHash, err := newHash(opts.ToCommit)
	if err != nil {
		return nil, fmt.Errorf("bad `to` commit hash `%s`: %s", opts.ToCommit, err)
	}

	toCommit, err := repository.CommitObject(toHash)
	if err != nil {
		return nil, fmt.Errorf("bad `to` commit `%s`: %s", opts.ToCommit, err)
	}

	hasSubmodules, err := HasSubmodulesInCommit(toCommit)
	if err != nil {
		return nil, err
	}

	patch := NewTmpPatchFile()

	fileHandler, err := os.OpenFile(patch.GetFilePath(), os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return nil, fmt.Errorf("cannot open patch file `%s`: %s", patch.GetFilePath(), err)
	}

	patchOpts := true_git.PatchOptions{
		FromCommit: opts.FromCommit,
		ToCommit:   opts.ToCommit,
		PathFilter: true_git.PathFilter{
			BasePath:     opts.BasePath,
			IncludePaths: opts.IncludePaths,
			ExcludePaths: opts.ExcludePaths,
		},
		WithEntireFileContext: opts.WithEntireFileContext,
		WithBinary:            opts.WithBinary,
	}

	var desc *true_git.PatchDescriptor
	if hasSubmodules {
		desc, err = true_git.PatchWithSubmodules(fileHandler, gitDir, workTreeCacheDir, patchOpts)
	} else {
		desc, err = true_git.Patch(fileHandler, gitDir, patchOpts)
	}

	if err != nil {
		return nil, fmt.Errorf("error creating patch between `%s` and `%s` commits: %s", opts.FromCommit, opts.ToCommit, err)
	}

	patch.Descriptor = desc

	err = fileHandler.Close()
	if err != nil {
		return nil, fmt.Errorf("error creating patch file `%s`: %s", patch.GetFilePath(), err)
	}

	return patch, nil
}

func HasSubmodulesInCommit(commit *object.Commit) (bool, error) {
	_, err := commit.File(".gitmodules")
	if err == object.ErrFileNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (repo *Base) createArchive(repoPath, gitDir, workTreeCacheDir string, opts ArchiveOptions) (Archive, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	commitHash, err := newHash(opts.Commit)
	if err != nil {
		return nil, fmt.Errorf("bad commit hash `%s`: %s", opts.Commit, err)
	}

	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("bad commit `%s`: %s", opts.Commit, err)
	}

	hasSubmodules, err := HasSubmodulesInCommit(commit)
	if err != nil {
		return nil, err
	}

	archive := NewTmpArchiveFile()

	fileHandler, err := os.OpenFile(archive.GetFilePath(), os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return nil, fmt.Errorf("cannot open archive file: %s", err)
	}

	archiveOpts := true_git.ArchiveOptions{
		Commit: opts.Commit,
		PathFilter: true_git.PathFilter{
			BasePath:     opts.BasePath,
			IncludePaths: opts.IncludePaths,
			ExcludePaths: opts.ExcludePaths,
		},
	}

	var desc *true_git.ArchiveDescriptor
	if hasSubmodules {
		desc, err = true_git.ArchiveWithSubmodules(fileHandler, gitDir, workTreeCacheDir, archiveOpts)
	} else {
		desc, err = true_git.Archive(fileHandler, gitDir, workTreeCacheDir, archiveOpts)
	}

	if err != nil {
		return nil, fmt.Errorf("error creating archive for commit `%s`: %s", opts.Commit, err)
	}

	archive.Descriptor = desc

	return archive, nil
}

func (repo *Base) isCommitExists(repoPath, gitDir string, commit string) (bool, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	commitHash, err := newHash(commit)
	if err != nil {
		return false, fmt.Errorf("bad commit hash `%s`: %s", commit, err)
	}

	_, err = repository.CommitObject(commitHash)
	if err == plumbing.ErrObjectNotFound {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("bad commit `%s`: %s", commit, err)
	}

	return true, nil
}

func (repo *Base) tagsList(repoPath string) ([]string, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	tags, err := repository.Tags()
	if err != nil {
		return nil, err
	}

	res := make([]string, 0)

	if err := tags.ForEach(func(ref *plumbing.Reference) error {
		obj, err := repository.TagObject(ref.Hash())
		switch err {
		case nil:
			res = append(res, obj.Name)
		case plumbing.ErrObjectNotFound:
			res = append(res, strings.TrimPrefix(ref.Name().String(), "refs/tags/"))
		default:
			// Some other error
			return err
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return res, nil
}

func (repo *Base) remoteBranchesList(repoPath string) ([]string, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	branches, err := repository.References()
	if err != nil {
		return nil, err
	}

	remoteBranchPrefix := "refs/remotes/origin/"

	res := make([]string, 0)
	err = branches.ForEach(func(r *plumbing.Reference) error {
		refName := r.Name().String()
		if strings.HasPrefix(refName, remoteBranchPrefix) {
			value := strings.TrimPrefix(refName, remoteBranchPrefix)
			if value != "HEAD" {
				res = append(res, value)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (repo *Base) checksum(repoPath, gitDir, workTreeCacheDir string, opts ChecksumOptions) (Checksum, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	commitHash, err := newHash(opts.Commit)
	if err != nil {
		return nil, fmt.Errorf("bad commit hash `%s`: %s", opts.Commit, err)
	}

	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("bad commit `%s`: %s", opts.Commit, err)
	}

	hasSubmodules, err := HasSubmodulesInCommit(commit)
	if err != nil {
		return nil, err
	}

	checksum := &ChecksumDescriptor{
		NoMatchPaths: make([]string, 0),
		Hash:         sha256.New(),
	}

	err = true_git.WithWorkTree(gitDir, workTreeCacheDir, opts.Commit, true_git.WithWorkTreeOptions{HasSubmodules: hasSubmodules}, func(workTreeDir string) error {
		paths := make([]string, 0)

		for _, pathPattern := range opts.Paths {
			res, err := getFilesByPattern(workTreeDir, filepath.Join(opts.BasePath, pathPattern))
			if err != nil {
				return fmt.Errorf("error getting files by path pattern `%s`: %s", pathPattern, err)
			}

			if len(res) == 0 {
				checksum.NoMatchPaths = append(checksum.NoMatchPaths, pathPattern)
				if debugChecksum() {
					logboek.LogF("Ignore checksum path pattern '%s': no matches found\n", pathPattern)
				}
			}

			paths = append(paths, res...)
		}

		sort.Strings(paths)

		pathFilter := true_git.PathFilter{
			BasePath:     opts.BasePath,
			IncludePaths: opts.IncludePaths,
			ExcludePaths: opts.ExcludePaths,
		}

		for _, path := range paths {
			fullPath := filepath.Join(workTreeDir, path)

			if filepath.Base(path) == ".git" {
				if debugChecksum() {
					logboek.LogF("Filter out service git path %s from checksum calculation\n", fullPath)
				}
				continue
			}

			if !pathFilter.IsFilePathValid(path) {
				if debugChecksum() {
					fmt.Fprintf(logboek.GetOutStream(), "Excluded file `%s` from resulting checksum by path filter %s\n", fullPath, pathFilter.String())
				}
				continue
			}

			_, err = checksum.Hash.Write([]byte(path))
			if err != nil {
				return fmt.Errorf("error calculating checksum of path `%s`: %s", path, err)
			}
			if debugChecksum() {
				logboek.LogF("Added file path '%s' to resulting checksum\n", path)
			}

			stat, err := os.Lstat(fullPath)
			// file should exist after being scanned
			if err != nil {
				return fmt.Errorf("error accessing file `%s`: %s", fullPath, err)
			}

			_, err = checksum.Hash.Write([]byte(fmt.Sprintf("%o", stat.Mode())))
			if err != nil {
				return fmt.Errorf("error calculating checksum of file `%s` mode: %s", fullPath, err)
			}
			if debugChecksum() {
				logboek.LogF("Added file %s mode %o to resulting checksum\n", fullPath, stat.Mode())
			}

			if stat.Mode().IsRegular() {
				f, err := os.Open(fullPath)
				if err != nil {
					return fmt.Errorf("unable to open file `%s`: %s", fullPath, err)
				}

				_, err = io.Copy(checksum.Hash, f)
				if err != nil {
					return fmt.Errorf("error calculating checksum of file `%s` content: %s", fullPath, err)
				}

				err = f.Close()
				if err != nil {
					return fmt.Errorf("error closing file `%s`: %s", fullPath, err)
				}

				if debugChecksum() {
					f, err := os.Open(fullPath)
					if err != nil {
						return fmt.Errorf("unable to open file `%s`: %s", fullPath, err)
					}

					hash := md5.New()
					_, err = io.Copy(hash, f)
					if err != nil {
						return fmt.Errorf("error reading file `%s` content: %s", fullPath, err)
					}
					contentHash := fmt.Sprintf("%x", hash.Sum(nil))

					err = f.Close()
					if err != nil {
						return fmt.Errorf("error closing file `%s`: %s", fullPath, err)
					}

					logboek.LogF("Added file '%s' to resulting checksum with content checksum: %s\n", fullPath, contentHash)
				}
			} else if stat.Mode()&os.ModeSymlink != 0 {
				linkname, err := os.Readlink(fullPath)
				if err != nil {
					return fmt.Errorf("cannot read symlink `%s`: %s", fullPath, err)
				}

				_, err = checksum.Hash.Write([]byte(linkname))
				if err != nil {
					return fmt.Errorf("error calculating checksum of symlink `%s`: %s", fullPath, err)
				}

				if debugChecksum() {
					logboek.LogF("Added symlink '%s' -> '%s' to resulting checksum\n", fullPath, linkname)
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if debugChecksum() {
		logboek.LogF("Calculated checksum %s\n", checksum.String())
	}

	return checksum, nil
}

func getFilesByPattern(baseDir, pathPattern string) ([]string, error) {
	fullPathPattern := filepath.Join(baseDir, pathPattern)

	matches, err := doublestar.Glob(fullPathPattern)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0)

	for _, fullPath := range matches {
		stat, err := os.Lstat(fullPath)
		// file should exist after glob match
		if err != nil {
			return nil, fmt.Errorf("error accessing file `%s`: %s", fullPath, err)
		}

		if stat.Mode().IsRegular() || (stat.Mode()&os.ModeSymlink != 0) {
			path, err := filepath.Rel(baseDir, fullPath)
			if err != nil {
				return nil, fmt.Errorf("error getting relative path for `%s`: %s", fullPath, err)
			}

			paths = append(paths, path)
		} else if stat.Mode().IsDir() {
			err := filepath.Walk(fullPath, func(fullWalkPath string, info os.FileInfo, accessErr error) error {
				if accessErr != nil {
					return fmt.Errorf("error accessing file `%s`: %s", fullWalkPath, err)
				}

				if info.Mode().IsRegular() || (stat.Mode()&os.ModeSymlink != 0) {
					path, err := filepath.Rel(baseDir, fullWalkPath)
					if err != nil {
						return fmt.Errorf("error getting relative path for `%s`: %s", fullWalkPath, err)
					}

					paths = append(paths, path)
				}

				return nil
			})

			if err != nil {
				return nil, fmt.Errorf("error scanning directory `%s`: %s", fullPath, err)
			}
		}
	}

	return paths, nil
}

func debugChecksum() bool {
	return os.Getenv("WERF_DEBUG_GIT_REPO_CHECKSUM") == "1"
}

func (repo *Base) checksumWithLsTree(repoPath, gitDir, workTreeCacheDir string, opts ChecksumOptions) (Checksum, error) {
	repository, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open repo `%s`: %s", repoPath, err)
	}

	commitHash, err := newHash(opts.Commit)
	if err != nil {
		return nil, fmt.Errorf("bad commit hash `%s`: %s", opts.Commit, err)
	}

	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("bad commit `%s`: %s", opts.Commit, err)
	}

	hasSubmodules, err := HasSubmodulesInCommit(commit)
	if err != nil {
		return nil, err
	}

	checksum := &ChecksumDescriptor{
		NoMatchPaths: make([]string, 0),
		Hash:         sha256.New(),
	}

	err = true_git.WithWorkTree(gitDir, workTreeCacheDir, opts.Commit, true_git.WithWorkTreeOptions{HasSubmodules: hasSubmodules}, func(worktreeDir string) error {
		repositoryWithPreparedWorktree, err := GitOpenWithCustomWorktreeDir(gitDir, worktreeDir)
		if err != nil {
			return err
		}

		// TODO merge opts.Paths into opts.IncludePaths
		pathFilter := NewLsTreePathFilter(
			opts.BasePath,
			append(opts.IncludePaths, opts.Paths...),
			opts.ExcludePaths,
		)

		h, err := LsTreeChecksum(repositoryWithPreparedWorktree, pathFilter)
		if err != nil {
			return err
		}

		checksum.Hash = h
		// TODO: checksum.NoMatchPaths

		return nil
	})

	if err != nil {
		return nil, err
	}

	if debugChecksum() {
		logboek.LogF("Calculated checksum %s\n", checksum.String())
	}

	return checksum, nil
}

func GitOpenWithCustomWorktreeDir(gitDir string, worktreeDir string) (*git.Repository, error) {
	worktreeFilesystem := osfs.New(worktreeDir)
	storage := filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault())
	return git.Open(storage, worktreeFilesystem)
}

func LsTreeChecksum(repository *git.Repository, pathFilter *LsTreePathFilter) (hash.Hash, error) {
	h := sha256.New()

	ref, err := repository.Head()
	if err != nil {
		return nil, err
	}

	commit, err := repository.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	if pathFilter.BasePath != "" {
		basePath := filepath.ToSlash(pathFilter.BasePath)
		entry, err := tree.FindEntry(basePath)
		if err != nil {
			return nil, err
		}

		// TODO: entry not found
		// TODO: entry file

		switch entry.Mode {
		case filemode.Dir:
			basePathTree, err := tree.Tree(basePath)
			if err != nil {
				return nil, err
			}

			tree = basePathTree
		case filemode.Submodule:
			submoduleRepository, submoduleTree, err := submoduleRepositoryAndTree(repository, basePath)
			if err != nil {
				return nil, err
			}

			repository = submoduleRepository
			tree = submoduleTree
		default:
		}
	}

	if pathFilter.ShouldNotWalkThroughTree() {
		h.Write([]byte(commit.Hash.String()))
	} else {
		if err = lsTreeChecksum(repository, tree, pathFilter.BasePath, pathFilter, h); err != nil {
			return nil, err
		}
	}

	return h, nil
}

func lsTreeChecksum(repository *git.Repository, tree *object.Tree, treePath string, pathFilter *LsTreePathFilter, h hash.Hash) error {
	for _, entry := range tree.Entries {
		entryPathRelativeToRepo := filepath.Join(treePath, entry.Name)

		switch entry.Mode {
		case filemode.Dir:
			isValid, shouldGoInside := pathFilter.CheckEntry(entry.Name)

			if !isValid {
				logboek.LogDebugLn("Skip dir", entryPathRelativeToRepo)
				continue
			}

			if !shouldGoInside {
				logboek.LogDebugLn("Use dir hash", entryPathRelativeToRepo)

				h.Write([]byte(entry.Hash.String()))
				continue
			} else {
				logboek.LogDebugLn("Go into dir", entryPathRelativeToRepo)

				entryTree, err := tree.Tree(entry.Name)
				if err != nil {
					return err
				}

				if err := pathFilter.WithoutEntryInPaths(entry.Name, func() error {
					return lsTreeChecksum(repository, entryTree, entryPathRelativeToRepo, pathFilter, h)
				}); err != nil {
					return err
				}
			}
		case filemode.Submodule:
			isValid, shouldGoInside := pathFilter.CheckEntry(entry.Name)

			if !isValid {
				logboek.LogDebugLn("Skip submodule", entryPathRelativeToRepo)
				continue
			}

			if !shouldGoInside {
				logboek.LogDebugLn("Use submodule hash", entryPathRelativeToRepo)

				h.Write([]byte(entry.Hash.String()))
				continue
			} else {
				logboek.LogDebugLn("Go into submodule", entryPathRelativeToRepo)

				submoduleRepository, submoduleTree, err := submoduleRepositoryAndTree(repository, entryPathRelativeToRepo)
				if err != nil {
					return err
				}

				if err := pathFilter.WithoutEntryInPaths(entry.Name, func() error {
					return lsTreeChecksum(submoduleRepository, submoduleTree, entryPathRelativeToRepo, pathFilter, h)
				}); err != nil {
					return err
				}
			}
		default:
			if pathFilter.IsFilePathValid(entryPathRelativeToRepo) {
				logboek.LogDebugLn("Add file", entryPathRelativeToRepo)
				h.Write([]byte(entry.Hash.String()))
			} else {
				logboek.LogDebugLn("Skip file", entryPathRelativeToRepo)
			}
		}
	}

	return nil
}

func submoduleRepositoryAndTree(repository *git.Repository, submoduleName string) (*git.Repository, *object.Tree, error) {
	worktree, err := repository.Worktree()
	if err != nil {
		return nil, nil, err
	}

	submodule, err := worktree.Submodule(submoduleName)
	if err != nil {
		return nil, nil, err
	}

	submoduleRepository, err := submodule.Repository()
	if err != nil {
		return nil, nil, err
	}

	ref, err := submoduleRepository.Head()
	if err != nil {
		return nil, nil, err
	}

	commit, err := submoduleRepository.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil, err
	}

	submoduleTree, err := commit.Tree()
	if err != nil {
		return nil, nil, err
	}

	return submoduleRepository, submoduleTree, nil
}
