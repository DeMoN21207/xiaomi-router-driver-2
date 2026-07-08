package update

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt string        `json:"published_at"`
	Assets      []GitHubAsset `json:"assets"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

var ErrReleaseAssetNotFound = errors.New("matching release asset not found")

func SelectAsset(release GitHubRelease, pattern string) (GitHubAsset, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return GitHubAsset{}, errors.New("asset pattern is empty")
	}

	for _, asset := range release.Assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" || strings.TrimSpace(asset.BrowserDownloadURL) == "" {
			continue
		}
		if assetMatchesPattern(name, pattern) {
			return asset, nil
		}
	}

	return GitHubAsset{}, fmt.Errorf("%w: %s", ErrReleaseAssetNotFound, pattern)
}

func assetMatchesPattern(name string, pattern string) bool {
	if name == pattern {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		matched, err := path.Match(pattern, name)
		return err == nil && matched
	}
	return false
}
