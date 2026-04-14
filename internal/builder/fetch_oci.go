package builder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/utils"
)

// maxConcurrentLayerPulls limits simultaneous layer downloads to avoid
// overwhelming Docker's CDN and causing EOF / RST drops on slow connections.
const maxConcurrentLayerPulls = 3

// layerDownloadRetries is the number of times to retry a failing layer download
// before giving up. Handles transient EOF / network blips from Docker's CDN.
const layerDownloadRetries = 3

type authChallenge struct {
	Realm   string
	Service string
	Scope   string
}

func parseAuthHeader(header string) authChallenge {
	// Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/ubuntu:pull"
	header = strings.TrimPrefix(header, "Bearer ")
	parts := strings.Split(header, ",")
	challenge := authChallenge{}
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 {
			val := strings.Trim(kv[1], "\"")
			switch kv[0] {
			case "realm":
				challenge.Realm = val
			case "service":
				challenge.Service = val
			case "scope":
				challenge.Scope = val
			}
		}
	}
	return challenge
}

func fetchToken(challenge authChallenge) (string, error) {
	url := fmt.Sprintf("%s?service=%s&scope=%s", challenge.Realm, challenge.Service, challenge.Scope)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.Token != "" {
		return data.Token, nil
	}
	return data.AccessToken, nil
}

type manifestV2 struct {
	SchemaVersion int `json:"schemaVersion"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

// downloadBlobWithRetry downloads a single blob to the persistent blobs storage.
func downloadBlobWithRetry(client *http.Client, blobURL, token, digest string, out io.Writer) error {
	blobsPath := filepath.Join(config.DataRoot, "blobs", "sha256")
	os.MkdirAll(blobsPath, 0755)
	targetPath := filepath.Join(blobsPath, digest)

	// Skip if already exists
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	}

	tmpTar := filepath.Join(config.DataRoot, "tmp-pull-"+digest)

	var lastErr error
	for attempt := 1; attempt <= layerDownloadRetries; attempt++ {
		if attempt > 1 {
			wait := time.Duration(attempt) * 2 * time.Second
			fmt.Fprintf(out, "[pull] blob %s download failed (%v), retrying in %s (attempt %d/%d)...\n",
				digest[:12], lastErr, wait, attempt, layerDownloadRetries)
			time.Sleep(wait)
		}

		req, err := http.NewRequest("GET", blobURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetch: %w", err)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}

		f, err := os.Create(tmpTar)
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}

		_, copyErr := io.Copy(f, resp.Body)
		f.Close()
		resp.Body.Close()

		if copyErr != nil {
			os.Remove(tmpTar)
			lastErr = fmt.Errorf("copy: %w", copyErr)
			continue
		}

		// Success - move to final location
		if err := os.Rename(tmpTar, targetPath); err != nil {
			os.Remove(tmpTar)
			lastErr = fmt.Errorf("rename: %w", err)
			continue
		}
		return nil
	}

	return fmt.Errorf("blob %s failed after %d attempts: %w", digest[:12], layerDownloadRetries, lastErr)
}

// downloadLayerWithRetry downloads a single blob to a temp file, retrying up to
// layerDownloadRetries times on transient errors (EOF, connection reset, etc.).
func downloadLayerWithRetry(client *http.Client, blobURL, token, digest string, out io.Writer) (string, error) {
	tmpTar := filepath.Join(config.DataRoot, "tmp-layer-"+digest+".tar.gz")

	var lastErr error
	for attempt := 1; attempt <= layerDownloadRetries; attempt++ {
		if attempt > 1 {
			// Exponential backoff: 2s, 4s
			wait := time.Duration(attempt) * 2 * time.Second
			fmt.Fprintf(out, "[base] layer %s download failed (%v), retrying in %s (attempt %d/%d)...\n",
				digest[:12], lastErr, wait, attempt, layerDownloadRetries)
			time.Sleep(wait)
		}

		req, err := http.NewRequest("GET", blobURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetch: %w", err)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}

		f, err := os.Create(tmpTar)
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}

		_, copyErr := io.Copy(f, resp.Body)
		f.Close()
		resp.Body.Close()

		if copyErr != nil {
			// Remove partial download so a retry starts fresh
			os.Remove(tmpTar)
			lastErr = fmt.Errorf("copy: %w", copyErr)
			continue
		}

		// Success
		return tmpTar, nil
	}

	return "", fmt.Errorf("layer %s failed after %d attempts: %w", digest[:12], layerDownloadRetries, lastErr)
}

// FetchOCIImage downloads an image from a Docker V2 registry and extracts its layers to destDir
func FetchOCIImage(imageRef, destDir string, out io.Writer) error {
	ref := utils.ParseImageRef(imageRef)

	registryURL := ref.Registry
	if !strings.HasPrefix(registryURL, "http") {
		registryURL = "https://" + registryURL
	}

	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, ref.Repo, ref.Tag)

	client := &http.Client{}
	req, err := http.NewRequest("GET", manifestURL, nil)
	if err != nil {
		return err
	}
	// Accept OCI and Docker V2 manifests
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json")

	fmt.Fprintf(out, "[base] fetching %s manifest...\n", imageRef)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	var token string
	if resp.StatusCode == 401 {
		authHeader := resp.Header.Get("Www-Authenticate")
		resp.Body.Close()

		challenge := parseAuthHeader(authHeader)
		// Force correct scope
		challenge.Scope = fmt.Sprintf("repository:%s:pull", ref.Repo)
		token, err = fetchToken(challenge)
		if err != nil {
			return fmt.Errorf("failed to get token: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = client.Do(req)
		if err != nil {
			return err
		}
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		return fmt.Errorf("failed to fetch manifest: %s", resp.Status)
	}

	var manifest manifestV2
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		resp.Body.Close()
		return err
	}
	resp.Body.Close()

	if manifest.SchemaVersion != 2 {
		return fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}

	// Deal with Multi-Arch Manifest Lists (Image Index)
	if len(manifest.Manifests) > 0 {
		fmt.Fprintf(out, "[base] %s is a manifest list, resolving amd64/linux...\n", imageRef)
		targetDigest := ""
		for _, m := range manifest.Manifests {
			if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
				targetDigest = m.Digest
				break
			}
		}
		if targetDigest == "" {
			// Fallback to first if amd64 not found
			targetDigest = manifest.Manifests[0].Digest
			fmt.Fprintf(out, "[base] warning: amd64 not found, falling back to %s\n", targetDigest)
		}

		// Re-fetch the specific manifest
		nestedURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, ref.Repo, targetDigest)
		reqNested, _ := http.NewRequest("GET", nestedURL, nil)
		reqNested.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
		if token != "" {
			reqNested.Header.Set("Authorization", "Bearer "+token)
		}

		respNested, err := client.Do(reqNested)
		if err != nil {
			return fmt.Errorf("failed to fetch nested manifest: %w", err)
		}
		defer respNested.Body.Close()

		if respNested.StatusCode != 200 {
			return fmt.Errorf("failed to fetch nested manifest: %s", respNested.Status)
		}
		if err := json.NewDecoder(respNested.Body).Decode(&manifest); err != nil {
			return err
		}
	}

	totalLayers := len(manifest.Layers)
	fmt.Fprintf(out, "[base] %s has %d layers\n", imageRef, totalLayers)

	// Create destDir (the consolidated rootfs)
	os.MkdirAll(destDir, 0755)

	type layerResult struct {
		index int
		path  string
		err   error
	}
	results := make(chan layerResult, totalLayers)

	// Semaphore to cap concurrent downloads — prevents EOF from CDN rate limiting
	sem := make(chan struct{}, maxConcurrentLayerPulls)

	for i, layer := range manifest.Layers {
		go func(idx int, l struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
		}) {
			sem <- struct{}{}        // acquire slot
			defer func() { <-sem }() // release slot

			fmt.Fprintf(out, "[base] pulling layer %d/%d (%s)...\n", idx+1, totalLayers, l.Digest[:12])

			blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, ref.Repo, l.Digest)
			tmpTar, err := downloadLayerWithRetry(client, blobURL, token, l.Digest, out)
			if err != nil {
				results <- layerResult{idx, "", err}
				return
			}
			results <- layerResult{idx, tmpTar, nil}
		}(i, layer)
	}

	// Collect all results (parallel downloads complete in any order)
	tempPaths := make([]string, totalLayers)
	for i := 0; i < totalLayers; i++ {
		res := <-results
		if res.err != nil {
			return res.err
		}
		tempPaths[res.index] = res.path
	}

	// Extract in ORDER (layer N overwrites layer N-1 — correct OCI semantics)
	for i, tmpTar := range tempPaths {
		fmt.Fprintf(out, "[base] extracting layer %d/%d...\n", i+1, totalLayers)
		err = utils.ExtractTarGz(tmpTar, destDir)
		os.Remove(tmpTar)
		if err != nil {
			return fmt.Errorf("failed to extract layer %d: %w", i+1, err)
		}
	}

	// Download config blob
	if manifest.Config.Digest != "" {
		fmt.Fprintf(out, "[base] fetching config blob %s...\n", manifest.Config.Digest[:12])
		configURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, ref.Repo, manifest.Config.Digest)
		req, _ := http.NewRequest("GET", configURL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			configPath := filepath.Join(destDir, "config.json")
			cf, err := os.Create(configPath)
			if err == nil {
				io.Copy(cf, resp.Body)
				cf.Close()
			}
		}
	}

	return nil
}

// PullOCIImage downloads an image from a Docker V2 registry and registers it in Minibox
func PullOCIImage(imageRef string, out io.Writer) error {
	ref := utils.ParseImageRef(imageRef)

	registryURL := ref.Registry
	if !strings.HasPrefix(registryURL, "http") {
		registryURL = "https://" + registryURL
	}

	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, ref.Repo, ref.Tag)

	client := &http.Client{}
	req, err := http.NewRequest("GET", manifestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json")

	fmt.Fprintf(out, "[pull] fetching %s manifest...\n", imageRef)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	var token string
	if resp.StatusCode == 401 {
		authHeader := resp.Header.Get("Www-Authenticate")
		resp.Body.Close()

		challenge := parseAuthHeader(authHeader)
		challenge.Scope = fmt.Sprintf("repository:%s:pull", ref.Repo)
		token, err = fetchToken(challenge)
		if err != nil {
			return fmt.Errorf("failed to get token: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = client.Do(req)
		if err != nil {
			return err
		}
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		return fmt.Errorf("failed to fetch manifest: %s", resp.Status)
	}

	var manifest manifestV2
	manifestData, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return err
	}

	// Deal with Multi-Arch Manifest Lists
	if len(manifest.Manifests) > 0 {
		fmt.Fprintf(out, "[pull] %s is a manifest list, resolving amd64/linux...\n", imageRef)
		targetDigest := ""
		for _, m := range manifest.Manifests {
			if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
				targetDigest = m.Digest
				break
			}
		}
		if targetDigest == "" {
			targetDigest = manifest.Manifests[0].Digest
			fmt.Fprintf(out, "[pull] warning: amd64 not found, falling back to %s\n", targetDigest)
		}

		nestedURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, ref.Repo, targetDigest)
		reqNested, _ := http.NewRequest("GET", nestedURL, nil)
		reqNested.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
		if token != "" {
			reqNested.Header.Set("Authorization", "Bearer "+token)
		}

		respNested, err := client.Do(reqNested)
		if err != nil {
			return fmt.Errorf("failed to fetch nested manifest: %w", err)
		}
		defer respNested.Body.Close()

		if respNested.StatusCode != 200 {
			return fmt.Errorf("failed to fetch nested manifest: %s", respNested.Status)
		}
		manifestData, _ = io.ReadAll(respNested.Body)
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			return err
		}
	}

	manifestDigest := utils.CalculateDigest(manifestData)
	fmt.Fprintf(out, "[pull] manifest: %s\n", manifestDigest[:12])

	// Save Manifest to blobs
	blobsPath := filepath.Join(config.DataRoot, "blobs", "sha256")
	os.MkdirAll(blobsPath, 0755)
	os.WriteFile(filepath.Join(blobsPath, manifestDigest), manifestData, 0644)

	// Pull layers and config in parallel
	totalItems := len(manifest.Layers)
	if manifest.Config.Digest != "" {
		totalItems++
	}

	results := make(chan error, totalItems)
	sem := make(chan struct{}, maxConcurrentLayerPulls)

	if manifest.Config.Digest != "" {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			digest := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
			fmt.Fprintf(out, "[pull] pulling config %s...\n", digest[:12])
			blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, ref.Repo, manifest.Config.Digest)
			results <- downloadBlobWithRetry(client, blobURL, token, digest, out)
		}()
	}

	for i, l := range manifest.Layers {
		go func(idx int, layer struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
		}) {
			sem <- struct{}{}
			defer func() { <-sem }()
			digest := strings.TrimPrefix(layer.Digest, "sha256:")
			fmt.Fprintf(out, "[pull] pulling layer %d/%d (%s)...\n", idx+1, len(manifest.Layers), digest[:12])
			blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, ref.Repo, layer.Digest)
			results <- downloadBlobWithRetry(client, blobURL, token, digest, out)
		}(i, l)
	}

	for i := 0; i < totalItems; i++ {
		if err := <-results; err != nil {
			return err
		}
	}

	// Update index.json
	fmt.Fprintf(out, "[pull] registering %s in index.json...\n", imageRef)
	if err := updateOCIIndex(imageRef, manifestDigest, int64(len(manifestData))); err != nil {
		return fmt.Errorf("failed to update index: %w", err)
	}

	fmt.Fprintf(out, "[pull] successfully pulled %s\n", imageRef)
	return nil
}
