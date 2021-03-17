package gitclone

import (
	"fmt"

	"github.com/bitrise-io/go-utils/command/git"
)

// CheckoutMethod is the checkout method used
type CheckoutMethod int

const (
	// InvalidCheckoutMethod ...
	InvalidCheckoutMethod CheckoutMethod = iota
	// CheckoutNoneMethod only adds remote, resets repo, updates submodules
	CheckoutNoneMethod
	// CheckoutCommitMethod checks out a given commit
	CheckoutCommitMethod
	// CheckoutTagMethod checks out a given tag
	CheckoutTagMethod
	// CheckoutBranchMethod checks out a given branch
	CheckoutBranchMethod
	// CheckoutPRMergeBranchMethod checks out a MR/PR (when merge branch is available)
	CheckoutPRMergeBranchMethod
	// CheckoutPRDiffFileMethod  checks out a MR/PR (when a diff file is available)
	CheckoutPRDiffFileMethod
	// CheckoutPRManualMergeMethod check out a Merge Request using manual merge
	CheckoutPRManualMergeMethod
	// CheckoutForkPRManualMergeMethod checks out a PR using manual merge
	CheckoutForkPRManualMergeMethod
	// CheckoutNoMergeSpecialHeadBranch checks out a MR/PR head branch only, without merging into base branch
	CheckoutNoMergeSpecialHeadBranch
	// CheckoutNoMergeForkBranch checks out a PR source branch, without merging
	CheckoutNoMergeForkBranch
)

// ParameterValidationError is returned when there is missing or malformatted parameter for a given parameter set
type ParameterValidationError struct {
	ErrorString string
}

// Error ...
func (e ParameterValidationError) Error() string {
	return e.ErrorString
}

// NewParameterValidationError return a new ValidationError
func NewParameterValidationError(msg string) error {
	return ParameterValidationError{ErrorString: msg}
}

// checkoutStrategy is the interface an actual checkout strategy implements
type checkoutStrategy interface {
	do(gitCmd git.Git, fetchOptions fetchOptions, fallback fallbackRetry) error
}

// X: required parameter
// !: used to identify checkout strategy
// _: optional parameter
// |==========================================================================================================================|
// | params\strat| commit | tag | branch | manualMR | manualPR | autoMerge | autoDiff | noMergeHeadBranch | noMergeForkBranch |
// | commit      |  X  !  |     |        |  X       |          |           |          |                   |                   |
// | tag         |        |  X !|        |          |          |           |          |                   |                   |
// | branch      |  _     |  _  |  X !   |  X       |  X       |  X        |          |                   |  X                |
// | branchDest  |        |     |        |  X       |  X       |           |  X       |                   |                   |
// | PRRepoURL   |        |     |        |      !   |  X !     |    !      |    !     |                   |  X !              |
// | PRID        |        |     |        |          |          |           |    !     |                   |    !              |
// | mergeBranch |        |     |        |          |          |  X !      |          |    !              |                   |
// | headBranch  |        |     |        |          |          |           |          |  X !              |                   |
// |==========================================================================================================================|

func selectCheckoutMethod(cfg Config) CheckoutMethod {
	isPR := cfg.PRRepositoryURL != "" || cfg.BranchDest != "" || cfg.PRMergeBranch != "" || cfg.PRID != 0
	if !isPR {
		if cfg.Commit != "" {
			return CheckoutCommitMethod
		}

		if cfg.Tag != "" {
			return CheckoutTagMethod
		}

		if cfg.Branch != "" {
			return CheckoutBranchMethod
		}

		return CheckoutNoneMethod
	}

	isFork := isFork(cfg.RepositoryURL, cfg.PRRepositoryURL)
	isPrivateSourceRepo := isPrivate(cfg.PRRepositoryURL)
	isPrivateFork := isFork && isPrivateSourceRepo
	isPublicFork := isFork && !isPrivateSourceRepo

	if !cfg.ShouldMergePR {
		if cfg.PRHeadBranch != "" {
			return CheckoutNoMergeSpecialHeadBranch
		}

		if !isFork {
			return CheckoutBranchMethod
		}

		if isPublicFork {
			return CheckoutNoMergeForkBranch
		}

		return CheckoutPRDiffFileMethod
	}

	if !cfg.ManualMerge || isPrivateFork {
		if cfg.PRMergeBranch != "" {
			return CheckoutPRMergeBranchMethod
		}

		return CheckoutPRDiffFileMethod
	}

	return CheckoutPRManualMergeMethod
}

func createCheckoutStrategy(checkoutMethod CheckoutMethod, cfg Config, patch patchSource) (checkoutStrategy, error) {
	switch checkoutMethod {
	case CheckoutNoneMethod:
		{
			return checkoutNone{}, nil
		}
	case CheckoutCommitMethod:
		{
			params, err := NewCommitParams(cfg.Commit, cfg.Branch)
			if err != nil {
				return nil, err
			}

			return checkoutCommit{
				params: *params,
			}, nil
		}
	case CheckoutTagMethod:
		{
			params, err := NewTagParams(cfg.Tag, cfg.Branch)
			if err != nil {
				return nil, err
			}

			return checkoutTag{
				params: *params,
			}, nil
		}
	case CheckoutBranchMethod:
		{
			params, err := NewBranchParams(cfg.Branch)
			if err != nil {
				return nil, err
			}

			return checkoutBranch{
				params: *params,
			}, nil
		}
	case CheckoutPRMergeBranchMethod:
		{
			params, err := NewPRMergeBranchParams(cfg.BranchDest, cfg.PRMergeBranch)
			if err != nil {
				return nil, err
			}

			return checkoutPRMergeBranch{
				params: *params,
			}, nil
		}
	case CheckoutPRDiffFileMethod:
		{
			patchFile, err := patch.getDiffPath(cfg.BuildURL, cfg.BuildAPIToken)
			if err != nil {
				return nil, fmt.Errorf("merging PR (automatic) failed, there is no Pull Request branch and could not download diff file: %v", err)
			}

			prManualMergeStrategy, err := createCheckoutStrategy(CheckoutPRManualMergeMethod, cfg, patch)
			if err != nil {
				return nil, err
			}

			params, err := NewPRDiffFileParams(cfg.BranchDest, prManualMergeStrategy)
			if err != nil {
				return nil, err
			}

			return checkoutPRDiffFile{
				params:    *params,
				patchFile: patchFile,
			}, nil
		}
	case CheckoutPRManualMergeMethod:
		{
			prRepositoryURL := ""
			if isFork(cfg.RepositoryURL, cfg.PRRepositoryURL) {
				prRepositoryURL = cfg.PRRepositoryURL
			}

			params, err := NewPRManualMergeParams(cfg.Branch, cfg.Commit, prRepositoryURL, cfg.BranchDest)
			if err != nil {
				return nil, err
			}

			return checkoutPRManualMerge{
				params: *params,
			}, nil
		}
	case CheckoutNoMergeSpecialHeadBranch:
		{
			params, err := NewCheckoutNoMergeSpecialHeadBranchParams(cfg.PRHeadBranch)
			if err != nil {
				return nil, err
			}

			return checkoutSpecialHeadBranch{
				params: *params,
			}, nil
		}
	case CheckoutNoMergeForkBranch:
		{
			params, err := NewCheckoutNoMergeForkBranchParams(cfg.Branch, cfg.PRRepositoryURL)
			if err != nil {
				return nil, err
			}

			return checkoutForkBranch{
				params: *params,
			}, nil
		}
	default:
		return nil, fmt.Errorf("invalid checkout strategy selected")
	}

}

func selectFetchOptions(checkoutStrategy CheckoutMethod, cloneDepth int, fetchAllTags bool) fetchOptions {
	opts := fetchOptions{
		depth:   cloneDepth,
		allTags: false,
	}

	switch checkoutStrategy {
	case CheckoutCommitMethod, CheckoutBranchMethod:
		opts.allTags = fetchAllTags
	case CheckoutTagMethod:
		opts.allTags = true
	default:
	}

	return opts
}

func selectFallbacks(checkoutStrategy CheckoutMethod, fetchOpts fetchOptions) fallbackRetry {
	if fetchOpts.IsFullDepth() {
		return nil
	}

	switch checkoutStrategy {
	case CheckoutBranchMethod:
		// the given branch's tip will be checked out, no need to unshallow
		return nil
	case CheckoutCommitMethod, CheckoutTagMethod:
		return simpleUnshallow{}
	case CheckoutPRMergeBranchMethod, CheckoutPRManualMergeMethod, CheckoutForkPRManualMergeMethod, CheckoutPRDiffFileMethod:
		return resetUnshallow{}
	default:
		return nil
	}
}
