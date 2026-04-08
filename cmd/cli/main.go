package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chaitu426/minibox/internal/utils"
)

func exitf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

// apiBase returns the daemon URL (override with MINIBOX_API, e.g. http://127.0.0.1:8080).
func apiBase() string {
	if b := os.Getenv("MINIBOX_API"); b != "" {
		return strings.TrimSuffix(strings.TrimSpace(b), "/")
	}
	return "http://127.0.0.1:8080"
}

func httpClient() *http.Client {
	// Keep a sane default so CLI doesn't hang forever.
	return &http.Client{Timeout: 60 * time.Second}
}

// apiDo sends the request, adding Authorization when MINIBOX_API_TOKEN is set.
func apiDo(req *http.Request) (*http.Response, error) {
	if t := strings.TrimSpace(os.Getenv("MINIBOX_API_TOKEN")); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return httpClient().Do(req)
}

func apiGET(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, apiBase()+path, nil)
	if err != nil {
		return nil, err
	}
	return apiDo(req)
}

func apiPOST(path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, apiBase()+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return apiDo(req)
}

// apiPOSTStream is for long-lived streaming endpoints (e.g. foreground run/build logs).
// It intentionally uses no client timeout so active streams are not cut mid-run.
func apiPOSTStream(path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, apiBase()+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if t := strings.TrimSpace(os.Getenv("MINIBOX_API_TOKEN")); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return (&http.Client{Timeout: 0}).Do(req)
}

func ping() {
	resp, err := apiGET("/ping")
	if err != nil {
		exitf(1, "Error connecting to daemon: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

func readHTTPError(resp *http.Response) string {
	if resp == nil {
		return "no response"
	}
	b, _ := io.ReadAll(resp.Body)
	if len(b) == 0 {
		return resp.Status
	}
	return strings.TrimSpace(string(b))
}

func printMainHelp() {
	utils.Banner()
	fmt.Println("Usage: minibox <command> [args]")
	fmt.Println()
	fmt.Println("Core:")
	fmt.Println("  run, exec, ps, logs, stop, kill, rm, stats")
	fmt.Println("Images:")
	fmt.Println("  build, images, save, load, rmi")
	fmt.Println("System:")
	fmt.Println("  ping, system prune")
	fmt.Println()
	fmt.Println("Global:")
	fmt.Println("  --help    Show this help")
	fmt.Println("  --version Show CLI version")
}

func buildCommand() {
	if len(os.Args) < 4 || os.Args[2] != "-t" {
		exitf(2, "Usage: minibox build -t <image> <path/to/dir>")
	}
	imageName := os.Args[3]
	dir := "."
	if len(os.Args) == 5 {
		dir = os.Args[4]
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		utils.PrintError("Failed to get absolute path: %v", err)
		return
	}

	miniBoxPath := filepath.Join(absDir, "MiniBox")
	miniBoxInfo, err := os.ReadFile(miniBoxPath)
	if err != nil {
		exitf(1, "Failed to read MiniBox file at %s: %v", miniBoxPath, err)
	}

	utils.PrintInfo("Building image %s from %s", imageName, filepath.Base(absDir))

	reqBody := map[string]string{
		"image":   imageName,
		"minibox": string(miniBoxInfo),
		"context": absDir,
	}

	jsonData, _ := json.Marshal(reqBody)

	resp, err := apiPOSTStream("/containers/build", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		exitf(1, "Connection to daemon failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Build failed: %s", readHTTPError(resp))
	}

	// Stream build logs with better formatting
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// New builder output is already structured and timed. Prefer faithful rendering.
		if strings.HasPrefix(line, "[") {
			// Highlight [prefix] messages.
			if parts := strings.SplitN(line, "] ", 2); len(parts) == 2 && strings.HasPrefix(parts[0], "[") {
				fmt.Println(utils.ColorBold + utils.ColorCyan + parts[0] + "]" + utils.ColorReset + " " + parts[1])
				continue
			}
			fmt.Println(line)
			continue
		}
		// Block-prefixed lines from parallel blocks: [block] ...
		if strings.HasPrefix(line, "[") && strings.Contains(line, "] ") {
			fmt.Println(line)
			continue
		}
		fmt.Println(line)
	}
	if err := scanner.Err(); err != nil {
		exitf(1, "Build stream error: %v", err)
	}
}

func logsCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox logs <containerID>")
	}
	resp, err := apiGET("/containers/logs?id=" + url.QueryEscape(os.Args[2]))
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func runCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox run [-d] [-m memoryMB] [-c cpuMax] [-p host:container] [-v host:container] [-e KEY=VAL] <image> [command...]")
	}

	detached := false
	memoryMB := 0
	cpuMax := 0
	portMap := map[string]string{}
	volumeMap := map[string]string{}
	var userEnv []string
	i := 2

	for i < len(os.Args) {
		switch os.Args[i] {
		case "-d":
			detached = true
			i++
		case "-m":
			i++
			if i >= len(os.Args) {
				exitf(2, "Error: -m requires a value (MB)")
			}
			memoryMB, _ = strconv.Atoi(os.Args[i])
			i++
		case "-c":
			i++
			if i >= len(os.Args) {
				exitf(2, "Error: -c requires a value")
			}
			cpuMax, _ = strconv.Atoi(os.Args[i])
			i++
		case "-p":
			i++
			if i >= len(os.Args) {
				exitf(2, "Error: -p requires an argument (host:container)")
			}
			parts := strings.SplitN(os.Args[i], ":", 2)
			if len(parts) != 2 {
				exitf(2, "Error: invalid port mapping %q (expected host:container)", os.Args[i])
			}
			portMap[parts[0]] = parts[1]
			i++
		case "-v", "--volume":
			i++
			if i >= len(os.Args) {
				exitf(2, "Error: -v requires an argument (host_path:container_path)")
			}
			parts := strings.SplitN(os.Args[i], ":", 2)
			if len(parts) != 2 {
				exitf(2, "Error: invalid volume mapping %q (expected host:container)", os.Args[i])
			}
			// Resolve absolute path for host mapping
			absHost, err := filepath.Abs(parts[0])
			if err != nil {
				exitf(1, "Error resolving absolute path for volume %q: %v", parts[0], err)
			}
			volumeMap[absHost] = parts[1]
			i++
		case "-e", "--env":
			i++
			if i >= len(os.Args) {
				exitf(2, "Error: -e requires an argument (KEY=VAL)")
			}
			userEnv = append(userEnv, os.Args[i])
			i++
		default:
			goto doneFlags
		}
	}
doneFlags:

	if i >= len(os.Args) {
		exitf(2, "Usage: minibox run [-d] [-m memoryMB] [-c cpuMax] [-p host:container] [-v host:container] [-e KEY=VAL] <image> [command...]")
	}

	image := os.Args[i]
	var cmdArgs []string
	if len(os.Args) > i+1 {
		cmdArgs = os.Args[i+1:]
	}

	reqBody := map[string]interface{}{
		"image":    image,
		"command":  cmdArgs,
		"memory":   memoryMB,
		"cpu":      cpuMax,
		"detached": detached,
		"ports":    portMap,
		"volumes":  volumeMap,
		"env":      userEnv,
	}

	jsonData, _ := json.Marshal(reqBody)

	utils.PrintInfo("Launching container for image %-s", image)

	postFn := apiPOST
	if !detached {
		postFn = apiPOSTStream
	}
	resp, err := postFn("/containers/run", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		exitf(1, "Failed to start container: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}

	// Stream output line-by-line for real-time display
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "[network]") {
			fmt.Println(utils.ColorBlue + line + utils.ColorReset)
		} else {
			fmt.Println(line)
		}
	}
	utils.PrintInfo("Container execution finished.")
}

func psCommand() {
	showAll := false
	jsonOut := false
	if len(os.Args) > 2 && os.Args[2] == "-a" {
		showAll = true
	}
	if len(os.Args) > 2 && os.Args[2] == "--json" {
		jsonOut = true
	}
	if len(os.Args) > 3 {
		for _, a := range os.Args[2:] {
			if a == "-a" {
				showAll = true
			}
			if a == "--json" {
				jsonOut = true
			}
		}
	}

	resp, err := apiGET("/containers")
	if err != nil {
		exitf(1, "Connection to daemon failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}

	var containers map[string]map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&containers)

	ids := make([]string, 0, len(containers))
	for id := range containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if jsonOut {
		type row struct {
			ID      string `json:"id"`
			Image   string `json:"image"`
			Command string `json:"command"`
			Created string `json:"created"`
			Status  string `json:"status"`
			Health  string `json:"health"`
			Exit    string `json:"exit"`
			Ports   string `json:"ports"`
		}
		outRows := []row{}
		for _, id := range ids {
			c := containers[id]
			status, _ := c["status"].(string)
			if !showAll && status != "running" {
				continue
			}
			img, _ := c["image"].(string)
			cmdStr, _ := c["command"].(string)
			created := ""
			if v, ok := c["created_at"].(string); ok {
				created = v
			}
			exitCode := ""
			if v, ok := c["exit_code"].(float64); ok {
				exitCode = fmt.Sprintf("%.0f", v)
			}
			if status == "running" {
				exitCode = "-"
			}
			ports := "-"
			if pm, ok := c["ports"].(map[string]any); ok && len(pm) > 0 {
				keys := make([]string, 0, len(pm))
				for k := range pm {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var pairs []string
				for _, hp := range keys {
					if cps, ok := pm[hp].(string); ok {
						pairs = append(pairs, "0.0.0.0:"+hp+"->"+cps+"/tcp")
					}
				}
				if len(pairs) > 0 {
					ports = strings.Join(pairs, ",")
				}
			}
			health := "none"
			if v, ok := c["health"].(string); ok && v != "" && status == "running" {
				health = v
			}
			outRows = append(outRows, row{
				ID: id, Image: img, Command: cmdStr, Created: created,
				Status: status, Health: health, Exit: exitCode, Ports: ports,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(outRows)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, utils.ColorBold+"CONTAINER ID\tIMAGE\tCOMMAND\tCREATED\tSTATUS\tHEALTH\tEXIT\tPORTS"+utils.ColorReset)

	count := 0
	for _, id := range ids {
		c := containers[id]
		status := c["status"].(string)
		if !showAll && status != "running" {
			continue
		}
		img := c["image"].(string)
		cmdStr := c["command"].(string)
		if len(cmdStr) > 25 {
			cmdStr = cmdStr[:22] + "..."
		}
		pidStr := fmt.Sprintf("%.0f", c["pid"].(float64))
		_ = pidStr // keep for future (debug) but don't show by default

		statusColor := utils.ColorWhite
		if status == "running" {
			statusColor = utils.ColorGreen
		} else {
			statusColor = utils.ColorRed
		}

		created := ""
		if v, ok := c["created_at"].(string); ok && v != "" {
			// RFC3339 from JSON time.Time; show relative-ish compact.
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				created = t.Format("2006-01-02 15:04:05")
			} else {
				created = v
			}
		}

		exitCode := ""
		if v, ok := c["exit_code"]; ok {
			switch vv := v.(type) {
			case float64:
				exitCode = fmt.Sprintf("%.0f", vv)
			case int:
				exitCode = fmt.Sprintf("%d", vv)
			}
		}
		if status == "running" {
			exitCode = "-"
		} else if exitCode == "" {
			exitCode = "?"
		}

		ports := "-"
		if pm, ok := c["ports"].(map[string]any); ok && len(pm) > 0 {
			keys := make([]string, 0, len(pm))
			for k := range pm {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var pairs []string
			for _, hp := range keys {
				if cps, ok := pm[hp].(string); ok {
					pairs = append(pairs, "0.0.0.0:"+hp+"->"+cps+"/tcp")
				}
			}
			if len(pairs) > 0 {
				ports = strings.Join(pairs, ",")
			}
		}

		health := "none"
		if v, ok := c["health"].(string); ok && v != "" {
			health = v
		}
		if status != "running" {
			health = "none"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, img, cmdStr, created, statusColor+status+utils.ColorReset, health, exitCode, ports)
		count++
	}
	w.Flush()
	if count == 0 {
		fmt.Println("No active containers found. Use -a to see all.")
	}
}

func imagesCommand() {
	jsonOut := len(os.Args) > 2 && os.Args[2] == "--json"
	resp, err := apiGET("/images")
	if err != nil {
		exitf(1, "Connection to daemon failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}

	type imageInfo struct {
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
	}
	var images []imageInfo
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		exitf(1, "Failed to parse image list: %v", err)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(images)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, utils.ColorBold+"REPOSITORY\tTAG\tIMAGE ID\tSIZE"+utils.ColorReset)
	for _, img := range images {
		size := "-"
		if img.SizeBytes > 0 {
			size = formatBytes(uint64(img.SizeBytes))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Name, "latest", "<none>", size)
	}
	w.Flush()
}

func saveCommand() {
	if len(os.Args) < 4 {
		exitf(2, "Usage: minibox save <image> <output.tar>")
	}
	image := os.Args[2]
	outPath, err := filepath.Abs(os.Args[3])
	if err != nil {
		exitf(1, "invalid output path: %v", err)
	}
	resp, err := apiPOST("/images/save?image="+url.QueryEscape(image)+"&path="+url.QueryEscape(outPath), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func loadCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox load <input.tar>")
	}
	inPath, err := filepath.Abs(os.Args[2])
	if err != nil {
		exitf(1, "invalid input path: %v", err)
	}
	resp, err := apiPOST("/images/load?path="+url.QueryEscape(inPath), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func rmiCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox rmi <image>")
	}
	resp, err := apiPOST("/images/remove?image="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func stopCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox stop [-t seconds] <containerID>")
	}
	id := os.Args[2]
	timeout := ""
	// Support: minibox stop -t 10 <id>
	if len(os.Args) >= 5 && os.Args[2] == "-t" {
		timeout = os.Args[3]
		id = os.Args[4]
	}
	path := "/containers/stop?id=" + url.QueryEscape(id)
	if timeout != "" {
		path += "&t=" + url.QueryEscape(timeout)
	}
	resp, err := apiPOST(path, "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func killCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox kill <containerID>")
	}
	resp, err := apiPOST("/containers/kill?id="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func rmCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox rm <containerID>")
	}
	resp, err := apiPOST("/containers/remove?id="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func progressBar(percent float64, width int) string {
	filled := int(percent / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	color := utils.ColorGreen
	if percent > 80 {
		color = utils.ColorRed
	} else if percent > 50 {
		color = utils.ColorYellow
	}

	bar := color + strings.Repeat("█", filled) + utils.ColorReset
	bar += strings.Repeat("░", width-filled)
	return bar
}

func statsCommand() {

	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox stats <containerID>")
	}
	id := os.Args[2]

	for {
		resp, err := apiGET("/containers/stats?id=" + url.QueryEscape(id))
		if err != nil {
			exitf(1, "Connection to daemon failed: %v", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// readHTTPError consumes the body; close then exit.
			msg := readHTTPError(resp)
			resp.Body.Close()
			exitf(1, "Error: %s", msg)
		}

		var s struct {
			MemoryUsage uint64  `json:"memory_usage"`
			MemoryLimit uint64  `json:"memory_limit"`
			CPUPercent  float64 `json:"cpu_percent"`
			Pids        uint64  `json:"pids"`
			NetInput    uint64  `json:"net_input"`
			NetOutput   uint64  `json:"net_output"`
			BlockInput  uint64  `json:"block_input"`
			BlockOutput uint64  `json:"block_output"`
		}
		json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()

		// Refresh UI: Clear screen and show a compact header
		fmt.Print("\033[H\033[2J")
		fmt.Printf(utils.ColorCyan+utils.ColorBold+"📊 LIVE STATS: %s "+utils.ColorReset+"(Ctrl+C to stop)\n\n", id)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		header := fmt.Sprintf("%sCPU %%\tMEM USAGE / LIMIT\tMEM %%\tNET I/O\tBLOCK I/O\tPIDS%s", utils.ColorBold, utils.ColorReset)
		fmt.Fprintln(w, header)

		memUsageStr := formatBytes(s.MemoryUsage)
		limitStr := "Unlimited"
		memPercent := 0.0
		if s.MemoryLimit > 0 {
			memPercent = (float64(s.MemoryUsage) / float64(s.MemoryLimit)) * 100.0
			limitStr = formatBytes(s.MemoryLimit)
		}

		bar := progressBar(memPercent, 20)
		netIO := fmt.Sprintf("%s / %s", formatBytes(s.NetInput), formatBytes(s.NetOutput))
		blockIO := fmt.Sprintf("%s / %s", formatBytes(s.BlockInput), formatBytes(s.BlockOutput))

		cpuColor := ""
		if s.CPUPercent > 80 {
			cpuColor = utils.ColorRed
		}

		fmt.Fprintf(w, "%s%.2f%%%s\t%s / %s\t%.2f%% %s\t%s\t%s\t%d\n",
			cpuColor, s.CPUPercent, utils.ColorReset,
			memUsageStr, limitStr, memPercent, bar,
			netIO, blockIO, s.Pids)
		w.Flush()

		time.Sleep(1 * time.Second)
	}
}

func systemCommand() {
	if len(os.Args) < 3 || os.Args[2] != "prune" {
		exitf(2, "Usage: minibox system prune")
	}

	resp, err := apiPOST("/system/prune", "application/json", nil)
	if err != nil {
		exitf(1, "Error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		exitf(1, "Failed to prune: %s", readHTTPError(resp))
	}

	var report struct {
		BlobsRemoved     int   `json:"blobs_removed"`
		BytesFreed       int64 `json:"bytes_freed"`
		FUSEMountsKilled int   `json:"fuse_mounts_killed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		fmt.Println("Failed to decode response:", err)
		return
	}

	utils.PrintSuccess("System Prune Completed:")
	fmt.Printf("  - Active FUSE mounts killed: %d\n", report.FUSEMountsKilled)
	fmt.Printf("  - Orphaned blobs removed:   %d\n", report.BlobsRemoved)
	fmt.Printf("  - Disk space recovered:     %s\n", formatBytes(uint64(report.BytesFreed)))
}

func execCommand() {
	if len(os.Args) < 4 {
		exitf(2, "Usage: minibox exec [-it] <containerID> <cmd...>")
	}

	i := 2
	interactive := false
	if os.Args[i] == "-it" || os.Args[i] == "-ti" {
		interactive = true
		i++
	}
	if len(os.Args) <= i+1 {
		exitf(2, "Usage: minibox exec [-it] <containerID> <cmd...>")
	}
	id := os.Args[i]
	cmdArgs := os.Args[i+1:]

	// Resolve PID from daemon state
	resp, err := apiGET("/containers")
	if err != nil {
		exitf(1, "Connection to daemon failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "Error: %s", readHTTPError(resp))
	}

	var containers map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		exitf(1, "Failed to parse daemon response: %v", err)
	}
	c, ok := containers[id]
	if !ok {
		exitf(1, "Container not found: %s", id)
	}
	pidF, ok := c["pid"].(float64)
	if !ok || pidF <= 0 {
		exitf(1, "Container PID not available for %s", id)
	}
	pid := fmt.Sprintf("%.0f", pidF)

	// Use nsenter for reliable namespace entry + TTY behavior.
	// This runs locally on the same host as the daemon.
	nsenterPath, err := exec.LookPath("nsenter")
	if err != nil {
		exitf(1, "nsenter not found (install util-linux). Cannot exec into container namespaces.")
	}

	args := []string{"-t", pid, "-m", "-u", "-n", "-i", "-p", "--"}
	args = append(args, cmdArgs...)
	cmd := exec.Command(nsenterPath, args...)
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		// Non-interactive: still print output.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		exitf(1, "exec failed: %v", err)
	}
}

func main() {
	if len(os.Args) < 2 {
		printMainHelp()
		return
	}

	command := os.Args[1]
	if command == "--help" || command == "-h" || command == "help" {
		printMainHelp()
		return
	}
	if command == "--version" || command == "version" {
		fmt.Println("minibox dev")
		return
	}

	switch command {
	case "ping":
		ping()
	case "build":
		buildCommand()
	case "run":
		runCommand()
	case "ps":
		psCommand()
	case "stats":
		statsCommand()
	case "logs":
		logsCommand()
	case "exec":
		execCommand()

	case "images":
		imagesCommand()
	case "save":
		saveCommand()
	case "load":
		loadCommand()
	case "rmi":
		rmiCommand()
	case "stop":
		stopCommand()
	case "kill":
		killCommand()
	case "rm":
		rmCommand()
	case "system":
		systemCommand()
	default:
		exitf(2, "Unknown command: %s", command)
	}
}
