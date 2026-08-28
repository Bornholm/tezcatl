package plugin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// InstallOptions installs a source plugin from a GitHub repository
// releasing goreleaser-style archives
// (<name>_<version>_<os>_<arch>.tar.gz plus an optional checksums.txt).
type InstallOptions struct {
	// Repo is the GitHub project: github.com/owner/repo, a full URL or
	// owner/repo.
	Repo string
	// Name selects the plugin when the release ships several
	// (tezcatl-source-<name>_… archives); optional otherwise.
	Name string
	// Version is a release tag (vX.Y.Z); empty means the latest release.
	Version string
	// Dir is the plugins directory.
	Dir string
	// OS/Arch select the artifact; they default to the current runtime.
	OS   string
	Arch string
	// APIBaseURL overrides https://api.github.com (tests).
	APIBaseURL string
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Install downloads the release archive, verifies it against
// checksums.txt when present, and installs the plugin binary into the
// plugins directory. It returns the installed plugin name.
func Install(ctx context.Context, opts InstallOptions) (string, error) {
	owner, repo, err := parseRepo(opts.Repo)
	if err != nil {
		return "", errors.WithStack(err)
	}

	baseURL := opts.APIBaseURL
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", baseURL, owner, repo)
	if opts.Version != "" {
		endpoint = fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", baseURL, owner, repo, url.PathEscape(opts.Version))
	}

	rel := release{}
	if err := getJSON(ctx, endpoint, &rel); err != nil {
		return "", errors.Wrapf(err, "could not resolve release of %s/%s", owner, repo)
	}

	assetName, assetURL, err := selectAsset(rel, opts.OS, opts.Arch, opts.Name)
	if err != nil {
		return "", errors.WithStack(err)
	}

	archive, err := download(ctx, assetURL)
	if err != nil {
		return "", errors.Wrapf(err, "could not download %s", assetName)
	}

	if err := verifyChecksum(ctx, rel, assetName, archive); err != nil {
		return "", errors.WithStack(err)
	}

	binaryName, binary, err := extractPluginBinary(archive, repo)
	if err != nil {
		return "", errors.WithStack(err)
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return "", errors.WithStack(err)
	}

	path := filepath.Join(opts.Dir, binaryName)
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		return "", errors.WithStack(err)
	}

	return strings.TrimPrefix(binaryName, Prefix), nil
}

func parseRepo(raw string) (string, string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "github.com/")

	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.Errorf("malformed repository %q (expected github.com/owner/repo)", raw)
	}

	return parts[0], parts[1], nil
}

// selectAsset picks the release archive for the target platform. When
// the release ships several plugins (tezcatl-source-<name>_… archives,
// like the main tezcatl repository), pluginName disambiguates.
func selectAsset(rel release, targetOS string, targetArch string, pluginName string) (string, string, error) {
	names := []string{}
	candidates := []int{}

	for i, asset := range rel.Assets {
		name := strings.ToLower(asset.Name)
		names = append(names, asset.Name)

		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}

		if strings.Contains(name, targetOS) && (strings.Contains(name, targetArch) || strings.Contains(name, archAlias(targetArch))) {
			candidates = append(candidates, i)
		}
	}

	if len(candidates) == 0 {
		return "", "", errors.Errorf("no %s/%s .tar.gz asset in release %s (assets: %s)", targetOS, targetArch, rel.TagName, strings.Join(names, ", "))
	}

	if pluginName != "" {
		prefix := Prefix + pluginName + "_"
		for _, i := range candidates {
			if strings.HasPrefix(rel.Assets[i].Name, prefix) {
				return rel.Assets[i].Name, rel.Assets[i].BrowserDownloadURL, nil
			}
		}

		return "", "", errors.Errorf("no plugin %q in release %s (plugins: %s)", pluginName, rel.TagName, strings.Join(pluginAssetNames(rel, candidates), ", "))
	}

	plugins := []int{}
	for _, i := range candidates {
		if strings.HasPrefix(rel.Assets[i].Name, Prefix) {
			plugins = append(plugins, i)
		}
	}

	switch len(plugins) {
	case 1:
		return rel.Assets[plugins[0]].Name, rel.Assets[plugins[0]].BrowserDownloadURL, nil
	case 0:
		// Dedicated plugin repository: a single conventional archive.
		return rel.Assets[candidates[0]].Name, rel.Assets[candidates[0]].BrowserDownloadURL, nil
	default:
		return "", "", errors.Errorf("release %s ships several plugins (%s): pass the plugin name, e.g. tezcatl plugin install <repo> <name>", rel.TagName, strings.Join(pluginAssetNames(rel, candidates), ", "))
	}
}

// pluginAssetNames lists the plugin names found among the candidate
// assets (tezcatl-source-<name>_… → <name>).
func pluginAssetNames(rel release, candidates []int) []string {
	names := []string{}

	for _, i := range candidates {
		name := rel.Assets[i].Name
		if !strings.HasPrefix(name, Prefix) {
			continue
		}

		if base, _, found := strings.Cut(strings.TrimPrefix(name, Prefix), "_"); found {
			names = append(names, base)
		}
	}

	return names
}

func archAlias(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64"
	case "386":
		return "i386"
	case "arm64":
		return "aarch64"
	default:
		return arch
	}
}

// verifyChecksum checks the archive against the checksums.txt asset of
// the release, when the release ships one.
func verifyChecksum(ctx context.Context, rel release, assetName string, archive []byte) error {
	var checksumsURL string

	for _, asset := range rel.Assets {
		if asset.Name == "checksums.txt" {
			checksumsURL = asset.BrowserDownloadURL
		}
	}

	if checksumsURL == "" {
		return nil
	}

	checksums, err := download(ctx, checksumsURL)
	if err != nil {
		return errors.Wrap(err, "could not download checksums.txt")
	}

	sum := sha256.Sum256(archive)
	expected := hex.EncodeToString(sum[:])

	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			if fields[0] != expected {
				return errors.Errorf("checksum mismatch for %s", assetName)
			}

			return nil
		}
	}

	return errors.Errorf("no checksum for %s in checksums.txt", assetName)
}

// extractPluginBinary finds the plugin binary in the archive: a file
// named tezcatl-source-*, or any executable file as fallback (then
// renamed following the convention, from the repository name).
func extractPluginBinary(archive []byte, repo string) (string, []byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return "", nil, errors.WithStack(err)
	}

	var (
		fallbackName string
		fallback     []byte
	)

	reader := tar.NewReader(gz)

	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return "", nil, errors.WithStack(err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Base(header.Name)

		if strings.HasPrefix(name, Prefix) {
			content, err := io.ReadAll(reader)
			if err != nil {
				return "", nil, errors.WithStack(err)
			}

			return name, content, nil
		}

		if header.FileInfo().Mode()&0o111 != 0 && fallback == nil {
			content, err := io.ReadAll(reader)
			if err != nil {
				return "", nil, errors.WithStack(err)
			}

			fallbackName = name
			fallback = content
		}
	}

	if fallback == nil {
		return "", nil, errors.Errorf("no %s* binary in the archive", Prefix)
	}

	// Rename following the convention, deriving the name from the
	// repository (tezcatl-source-foo, tezcatl-plugin-foo or foo → foo).
	name := strings.TrimPrefix(strings.TrimPrefix(repo, Prefix), "tezcatl-plugin-")
	_ = fallbackName

	return Prefix + name, fallback, nil
}

func getJSON(ctx context.Context, endpoint string, into any) error {
	body, err := download(ctx, endpoint)
	if err != nil {
		return errors.WithStack(err)
	}

	if err := json.Unmarshal(body, into); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func download(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	req.Header.Set("Accept", "application/octet-stream, application/vnd.github+json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status %d for %s", res.StatusCode, endpoint)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return body, nil
}
