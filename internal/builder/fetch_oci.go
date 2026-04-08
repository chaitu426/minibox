package builder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/utils"
)

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

	fmt.Fprintf(out, "[base] %s has %d layers\n", imageRef, len(manifest.Layers))
	
	// Create destDir (the consolidated rootfs)
	os.MkdirAll(destDir, 0755)

	type layerResult struct {
		index   int
		path    string
		err     error
	}
	results := make(chan layerResult, len(manifest.Layers))

	for i, layer := range manifest.Layers {
		go func(idx int, l struct {
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
			Digest    string `json:"digest"`
		}) {
			fmt.Fprintf(out, "[base] pulling layer %d/%d (%s)...\n", idx+1, len(manifest.Layers), l.Digest[:12])
			
			blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, ref.Repo, l.Digest)
			req, _ := http.NewRequest("GET", blobURL, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}

			resp, err := client.Do(req)
			if err != nil {
				results <- layerResult{idx, "", fmt.Errorf("failed to fetch layer %s: %w", l.Digest, err)}
				return
			}
			defer resp.Body.Close()
			
			if resp.StatusCode != 200 {
				results <- layerResult{idx, "", fmt.Errorf("failed to download layer %s: %s", l.Digest, resp.Status)}
				return
			}

			// Download to temp tarball
			tmpTar := filepath.Join(config.DataRoot, "tmp-layer-"+l.Digest+".tar.gz")
			f, err := os.Create(tmpTar)
			if err != nil {
				results <- layerResult{idx, "", err}
				return
			}
			_, err = io.Copy(f, resp.Body)
			f.Close()
			if err != nil {
				results <- layerResult{idx, "", err}
				return
			}
			results <- layerResult{idx, tmpTar, nil}
		}(i, layer)
	}

	// We must extract in ORDER
	tempPaths := make([]string, len(manifest.Layers))
	for i := 0; i < len(manifest.Layers); i++ {
		res := <-results
		if res.err != nil {
			return res.err
		}
		tempPaths[res.index] = res.path
	}

	for i, tmpTar := range tempPaths {
		fmt.Fprintf(out, "[base] extracting layer %d/%d...\n", i+1, len(manifest.Layers))
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
