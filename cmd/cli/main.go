package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/chaitu426/minibox/internal/compose"
	"github.com/chaitu426/minibox/internal/models"
	"github.com/chaitu426/minibox/internal/utils"
	"github.com/chaitu426/minibox/internal/version"
)

func exitf(code int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if strings.HasPrefix(msg, "[") {
		fmt.Fprintln(os.Stderr, msg)
	} else {
		fmt.Fprintf(os.Stderr, "[error] %s\n", msg)
	}
	os.Exit(code)
}

func apiBase() string {
	if b := os.Getenv("MINIBOX_API"); b != "" {
		return strings.TrimSuffix(strings.TrimSpace(b), "/")
	}
	return "http://127.0.0.1:8080"
}

func httpClient() *http.Client {
	// Set default timeout. CLI hang nako vhayla.
	return &http.Client{Timeout: 60 * time.Second}
}

// Send request. Add token if present
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

func apiDial() (net.Conn, error) {
	u, err := url.Parse(apiBase())
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	return net.Dial("tcp", host)
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

// For long streaming (logs/build). No timeout required bhau.
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
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

type interactiveReader struct {
	jsonPart io.Reader
	stdin    io.Reader
}

func (r *interactiveReader) Read(p []byte) (int, error) {
	n, err := r.jsonPart.Read(p)
	if err == io.EOF {
		return r.stdin.Read(p)
	}
	return n, err
}

func printMainHelp() {
	utils.Banner()
	fmt.Println("  run, exec, ps, logs, stop, kill, rm, stats")
	fmt.Println("  compose [up|down|ps]")
	fmt.Println("Images:")
	fmt.Println("  build, images, save, load, rmi")
	fmt.Println("System:")
	fmt.Println("  ping, system prune")
	fmt.Println()
	fmt.Println("Global:")
	fmt.Println("  --help    Show this help")
	fmt.Println("  --version Show CLI version")
}

func triggerBuild(imageName, dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %v", err)
	}

	miniBoxPath := filepath.Join(absDir, "MiniBox")
	miniBoxInfo, err := os.ReadFile(miniBoxPath)
	if err != nil {
		return fmt.Errorf("failed to read MiniBox file at %s: %v", miniBoxPath, err)
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
		return fmt.Errorf("connection to daemon failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("build failed: %s", readHTTPError(resp))
	}

	// Stream build logs
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "[") {
			if parts := strings.SplitN(line, "] ", 2); len(parts) == 2 && strings.HasPrefix(parts[0], "[") {
				fmt.Println(utils.ColorBold + utils.ColorCyan + parts[0] + "]" + utils.ColorReset + " " + parts[1])
				continue
			}
		}
		fmt.Println(line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("build stream error: %v", err)
	}
	return nil
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

	if err := triggerBuild(imageName, dir); err != nil {
		exitf(1, "%v", err)
	}
}

func logsCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox logs <containerID>")
	}
	resp, err := apiGET("/containers/logs?id=" + url.QueryEscape(os.Args[2]))
	if err != nil {
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func runCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox run [-d] [-m memoryMB] [-c cpuMax] [-p host:container] [-v host:container] [-e KEY=VAL] <image> [command...]")
	}

	detached := false
	interactive := false
	memoryMB := 0
	cpuMax := 0
	portMap := map[string]string{}
	volumeMap := map[string]string{}
	var userEnv []string
	user := ""
	i := 2

	for i < len(os.Args) {
		switch os.Args[i] {
		case "-d":
			detached = true
			i++
		case "-it", "-ti":
			interactive = true
			detached = false
			i++
		case "-i":
			interactive = true
			i++
		case "-t":
			// TTY (handled via interactive)
			i++
		case "-m":
			i++
			if i >= len(os.Args) {
				exitf(2, "-m requires a value (MB)")
			}
			memoryMB, _ = strconv.Atoi(os.Args[i])
			i++
		case "-c":
			i++
			if i >= len(os.Args) {
				exitf(2, "-c requires a value")
			}
			cpuMax, _ = strconv.Atoi(os.Args[i])
			i++
		case "-p":
			i++
			if i >= len(os.Args) {
				exitf(2, "-p requires an argument (host:container)")
			}
			parts := strings.SplitN(os.Args[i], ":", 2)
			if len(parts) != 2 {
				exitf(2, "invalid port mapping %q (expected host:container)", os.Args[i])
			}
			portMap[parts[0]] = parts[1]
			i++
		case "-v", "--volume":
			i++
			if i >= len(os.Args) {
				exitf(2, "-v requires an argument (host_path:container_path)")
			}
			parts := strings.SplitN(os.Args[i], ":", 2)
			if len(parts) != 2 {
				exitf(2, "invalid volume mapping %q (expected host:container)", os.Args[i])
			}
			// Resolve absolute path for host mapping
			absHost, err := filepath.Abs(parts[0])
			if err != nil {
				exitf(1, "resolving absolute path for volume %q: %v", parts[0], err)
			}
			volumeMap[absHost] = parts[1]
			i++
		case "-e", "--env":
			i++
			if i >= len(os.Args) {
				exitf(2, "-e requires an argument (KEY=VAL)")
			}
			userEnv = append(userEnv, os.Args[i])
			i++
		case "-u", "--user":
			i++
			if i >= len(os.Args) {
				exitf(2, "--user requires a value")
			}
			user = os.Args[i]
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
		"image":       image,
		"command":     cmdArgs,
		"memory":      memoryMB,
		"cpu":         cpuMax,
		"detached":    detached,
		"interactive": interactive,
		"ports":       portMap,
		"volumes":     volumeMap,
		"env":         userEnv,
		"user":        user,
	}

	jsonData, _ := json.Marshal(reqBody)

	utils.PrintInfo("Launching container for image %-s", image)

	if interactive && !detached {
		// Set host terminal to raw mode for interaction
		oldState, err := utils.SetRaw(os.Stdin.Fd())
		if err == nil {
			defer utils.Restore(os.Stdin.Fd(), oldState)
		}

		conn, err := apiDial()
		if err != nil {
			exitf(1, "Failed to connect to daemon: %v", err)
		}
		defer conn.Close()

		// Send Hijack request
		fmt.Fprintf(conn, "POST /containers/run HTTP/1.1\r\n")
		fmt.Fprintf(conn, "Host: %s\r\n", "minibox")
		fmt.Fprintf(conn, "Content-Type: application/json\r\n")
		if t := strings.TrimSpace(os.Getenv("MINIBOX_API_TOKEN")); t != "" {
			fmt.Fprintf(conn, "Authorization: Bearer %s\r\n", t)
		}
		fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(jsonData))
		conn.Write(jsonData)

		// Read response headers
		br := bufio.NewReader(conn)
		line, _ := br.ReadString('\n')
		if !strings.Contains(line, "200") {
			// Read rest of headers
			for {
				l, _ := br.ReadString('\n')
				if l == "\r\n" || l == "\n" || l == "" {
					break
				}
			}
			body, _ := io.ReadAll(br)
			exitf(1, "Failed to start container: %s %s", line, string(body))
		}

		// Consume rest of headers
		for {
			l, _ := br.ReadString('\n')
			if l == "\r\n" || l == "\n" || l == "" {
				break
			}
		}

		// Now we have raw bi-directional I/O
		errCh := make(chan error, 2)
		go func() {
			_, err := io.Copy(conn, os.Stdin)
			errCh <- err
		}()
		go func() {
			_, err := io.Copy(os.Stdout, br)
			errCh <- err
		}()

		<-errCh
	} else {
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
			exitf(1, "%s", readHTTPError(resp))
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
	}
	utils.PrintInfo("Container execution finished.")
}

func dbCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox db [run|shell] ...")
	}
	switch os.Args[2] {
	case "run":
		// handled below
	case "shell", "console":
		if len(os.Args) < 4 {
			exitf(2, "Usage: minibox db shell <containerID>")
		}
		dbShell(os.Args[3])
		return
	default:
		exitf(2, "Usage: minibox db [run|shell] ...")
	}

	detached := true // DBs run detached by default
	name := ""
	dataPath := ""
	cmdStr := ""
	shmSizeMB := 256 // Default SHM for DBs
	portMap := map[string]string{}
	var userEnv []string
	user := ""

	i := 3
	for i < len(os.Args) {
		switch os.Args[i] {
		case "--name":
			i++
			if i >= len(os.Args) {
				exitf(2, "--name requires a value")
			}
			name = os.Args[i]
			i++
		case "--data":
			i++
			if i >= len(os.Args) {
				exitf(2, "--data requires a value (container path)")
			}
			dataPath = os.Args[i]
			i++
		case "--cmd":
			i++
			if i >= len(os.Args) {
				exitf(2, "--cmd requires a value")
			}
			cmdStr = os.Args[i]
			i++
		case "--shm-size":
			i++
			if i >= len(os.Args) {
				exitf(2, "--shm-size requires a value in MB")
			}
			if v, err := strconv.Atoi(os.Args[i]); err == nil && v > 0 {
				shmSizeMB = v
			} else {
				exitf(2, "--shm-size must be a positive integer (MB)")
			}
			i++
		case "-p":
			i++
			if i >= len(os.Args) {
				exitf(2, "-p requires an argument (host:container)")
			}
			parts := strings.SplitN(os.Args[i], ":", 2)
			if len(parts) != 2 {
				exitf(2, "invalid port mapping %q (expected host:container)", os.Args[i])
			}
			portMap[parts[0]] = parts[1]
			i++
		case "-e", "--env":
			i++
			if i >= len(os.Args) {
				exitf(2, "-e requires an argument (KEY=VAL)")
			}
			userEnv = append(userEnv, os.Args[i])
			i++
		case "-u", "--user":
			i++
			if i >= len(os.Args) {
				exitf(2, "--user requires a value")
			}
			user = os.Args[i]
			i++
		case "-d":
			// accepted for symmetry; db run is detached by default
			detached = true
			i++
		case "-it", "-i", "-t":
			detached = false
			i++
		case "--rm":
			i++
		default:
			goto doneFlags
		}
	}
doneFlags:

	if i >= len(os.Args) {
		exitf(2, "Usage: minibox db run [--name name] [--data /container/path] [--cmd \"...\"] [-p host:container] [-e KEY=VAL] <image> [command...]")
	}

	image := os.Args[i]
	var cmdArgs []string
	if len(os.Args) > i+1 {
		cmdArgs = os.Args[i+1:]
	}
	if len(cmdArgs) == 0 && cmdStr != "" {
		// Keep parsing minimal: use /bin/sh -c for a single command string.
		cmdArgs = []string{"/bin/sh", "-c", cmdStr}
	}

	if name == "" {
		// Default name from image
		name = strings.ReplaceAll(image, ":", "-")
		name = strings.ReplaceAll(name, ".", "-")
	}
	if dataPath == "" {
		// Generic default; user should override for Postgres/MySQL etc.
		dataPath = "/var/lib/minibox-data"
	}

	// Named volume (DataRoot/volumes/<name>)
	namedVolumes := map[string]string{
		name + "-data": dataPath,
	}

	// DB-friendly defaults (best effort; can be extended later):
	// - prefer to survive OOM
	// - give higher IO weight
	// - larger /dev/shm for shared_buffers (Postgres) / wiredTiger cache (Mongo)
	reqBody := map[string]interface{}{
		"image":         image,
		"command":       cmdArgs,
		"detached":      detached,
		"interactive":   !detached, // db run -it sets detached=false
		"ports":         portMap,
		"named_volumes": namedVolumes,
		"env":           userEnv,
		"db_mode":       true,
		"user":          user,
		"shm_size":      shmSizeMB,
		"oom_score_adj": -900, // DBs are important
	}

	jsonData, _ := json.Marshal(reqBody)

	modeStr := ""
	if !detached {
		modeStr = "interactive "
	}
	utils.PrintInfo("Launching %sdatabase container image=%s volume=%s -> %s", modeStr, image, name+"-data", dataPath)

	if !detached {
		// Set host terminal to raw mode for interaction
		oldState, err := utils.SetRaw(os.Stdin.Fd())
		if err == nil {
			defer utils.Restore(os.Stdin.Fd(), oldState)
		}

		conn, err := apiDial()
		if err != nil {
			exitf(1, "Failed to connect to daemon: %v", err)
		}
		defer conn.Close()

		// Send Hijack request
		fmt.Fprintf(conn, "POST /containers/run HTTP/1.1\r\n")
		fmt.Fprintf(conn, "Host: %s\r\n", "minibox")
		fmt.Fprintf(conn, "Content-Type: application/json\r\n")
		if t := strings.TrimSpace(os.Getenv("MINIBOX_API_TOKEN")); t != "" {
			fmt.Fprintf(conn, "Authorization: Bearer %s\r\n", t)
		}
		fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(jsonData))
		conn.Write(jsonData)

		// Read response headers
		br := bufio.NewReader(conn)
		line, _ := br.ReadString('\n')
		if !strings.Contains(line, "200") {
			// Read rest of headers
			for {
				l, _ := br.ReadString('\n')
				if l == "\r\n" || l == "\n" || l == "" {
					break
				}
			}
			body, _ := io.ReadAll(br)
			exitf(1, "Failed to start container: %s %s", line, string(body))
		}

		// Consume rest of headers
		for {
			l, _ := br.ReadString('\n')
			if l == "\r\n" || l == "\n" || l == "" {
				break
			}
		}

		// Now we have raw bi-directional I/O
		errCh := make(chan error, 2)
		go func() {
			_, err := io.Copy(conn, os.Stdin)
			errCh <- err
		}()
		go func() {
			_, err := io.Copy(os.Stdout, br) // Note: use br to get any data already read into buffer
			errCh <- err
		}()

		<-errCh
	} else {
		resp, err := apiPOST("/containers/run", "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			exitf(1, "Failed to start db container: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			exitf(1, "%s", readHTTPError(resp))
		}
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Container started (detached): %s\n", string(body))
	}
}

// readProcEnvVar reads env from /proc/<pid>/environ
func readProcEnvVar(pid int, key string) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, entry := range bytes.Split(data, []byte{'\x00'}) {
		if strings.HasPrefix(string(entry), prefix) {
			return string(entry[len(prefix):])
		}
	}
	return ""
}

func dbShell(id string) {
	// 1. Resolve image to determine CLI type
	resp, err := apiGET("/containers")
	if err != nil {
		exitf(1, "Failed to connect to daemon: %v", err)
	}
	defer resp.Body.Close()
	var containers map[string]map[string]any
	json.NewDecoder(resp.Body).Decode(&containers)
	c, ok := containers[id]
	if !ok {
		exitf(1, "Container not found: %s", id)
	}
	image, _ := c["image"].(string)

	// Extract the container process PID so we can read its environment.
	pid := 0
	if pidF, ok := c["pid"].(float64); ok {
		pid = int(pidF)
	}

	var shellCmd []string
	if strings.Contains(image, "redis") {
		// Try PATH first, then usual absolute paths (some minimal images omit redis-cli).
		shellCmd = []string{"/bin/sh", "-c",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; " +
				"command -v redis-cli >/dev/null 2>&1 && exec redis-cli; " +
				"[ -x /usr/local/bin/redis-cli ] && exec /usr/local/bin/redis-cli; " +
				"[ -x /usr/bin/redis-cli ] && exec /usr/bin/redis-cli; " +
				"echo 'redis-cli not found in container (image may be server-only). On the host: redis-cli -h 127.0.0.1 -p <port>' >&2; exit 127"}
	} else if strings.Contains(image, "postgres") {
		shellCmd = []string{"/bin/sh", "-c", "psql -U postgres"}
	} else if strings.Contains(image, "mongo") {
		// Read credentials from the running mongod process environment (accessible
		// via /proc/<pid>/environ even across namespaces).
		mongoUser := readProcEnvVar(pid, "MONGO_INITDB_ROOT_USERNAME")
		mongoPass := readProcEnvVar(pid, "MONGO_INITDB_ROOT_PASSWORD")

		// Locate mongosh
		const findMongosh = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; " +
			"MONGOSH=$(command -v mongosh 2>/dev/null); " +
			"[ -z \"$MONGOSH\" ] && for p in /usr/bin/mongosh /usr/local/bin/mongosh /opt/mongosh/bin/mongosh; do [ -x \"$p\" ] && MONGOSH=\"$p\" && break; done; " +
			"[ -z \"$MONGOSH\" ] && { echo \"mongosh not found in container\" >&2; exec /bin/sh; }"

		var mongoshArgs string
		if mongoUser != "" {
			mongoshArgs = fmt.Sprintf(" --username %q --password %q --authenticationDatabase admin", mongoUser, mongoPass)
		}
		shellCmd = []string{"/bin/sh", "-c", findMongosh + "; exec \"$MONGOSH\"" + mongoshArgs}
	} else if strings.Contains(image, "mysql") || strings.Contains(image, "mariadb") {
		shellCmd = []string{"/bin/sh", "-c", "mysql -uroot"}
	} else {
		shellCmd = []string{"/bin/sh"}
	}

	utils.PrintInfo("Opening database console for %s...", id)
	execWithArgs(id, shellCmd, true)
}

func execWithArgs(id string, cmdArgs []string, interactive bool) {
	reqBody := map[string]any{
		"id":          id,
		"command":     cmdArgs,
		"interactive": interactive,
	}
	jsonData, _ := json.Marshal(reqBody)

	if interactive {
		oldState, err := utils.SetRaw(os.Stdin.Fd())
		if err == nil {
			defer utils.Restore(os.Stdin.Fd(), oldState)
		}

		conn, err := apiDial()
		if err != nil {
			exitf(1, "Failed to connect to daemon: %v", err)
		}
		defer conn.Close()

		fmt.Fprintf(conn, "POST /containers/exec HTTP/1.1\r\n")
		fmt.Fprintf(conn, "Host: %s\r\n", "minibox")
		fmt.Fprintf(conn, "Content-Type: application/json\r\n")
		if t := strings.TrimSpace(os.Getenv("MINIBOX_API_TOKEN")); t != "" {
			fmt.Fprintf(conn, "Authorization: Bearer %s\r\n", t)
		}
		fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(jsonData))
		conn.Write(jsonData)

		br := bufio.NewReader(conn)
		line, _ := br.ReadString('\n')
		if !strings.Contains(line, "200") {
			for {
				l, _ := br.ReadString('\n')
				if l == "\r\n" || l == "\n" || l == "" {
					break
				}
			}
			body, _ := io.ReadAll(br)
			exitf(1, "Exec failed: %s %s", line, string(body))
		}

		for {
			l, _ := br.ReadString('\n')
			if l == "\r\n" || l == "\n" || l == "" {
				break
			}
		}

		errCh := make(chan error, 2)
		go func() {
			_, err := io.Copy(conn, os.Stdin)
			errCh <- err
		}()
		go func() {
			_, err := io.Copy(os.Stdout, br)
			errCh <- err
		}()
		<-errCh
	} else {
		resp, err := apiPOST("/containers/exec", "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			exitf(1, "Exec failed: %v", err)
		}
		defer resp.Body.Close()
		io.Copy(os.Stdout, resp.Body)
	}
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
		exitf(1, "%s", readHTTPError(resp))
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
		exitf(1, "%s", readHTTPError(resp))
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
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
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
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func rmiCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox rmi <image>")
	}
	resp, err := apiPOST("/images/remove?image="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
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
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func killCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox kill <containerID>")
	}
	resp, err := apiPOST("/containers/kill?id="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
	}
	io.Copy(os.Stdout, resp.Body)
}

func rmCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox rm <containerID>")
	}
	resp, err := apiPOST("/containers/remove?id="+url.QueryEscape(os.Args[2]), "application/x-www-form-urlencoded", nil)
	if err != nil {
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitf(1, "%s", readHTTPError(resp))
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
			exitf(1, "%s", msg)
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
		fmt.Printf(utils.ColorCyan+utils.ColorBold+"STATS: %s "+utils.ColorReset+"(Ctrl+C to stop)\n\n", id)

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
		exitf(2, "Usage: minibox system prune [--build-cache]")
	}

	path := "/system/prune"
	if len(os.Args) > 3 && os.Args[3] == "--build-cache" {
		path += "?build_cache=1"
	}
	resp, err := apiPOST(path, "application/json", nil)
	if err != nil {
		exitf(1, "%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		exitf(1, "Failed to prune: %s", readHTTPError(resp))
	}

	var report struct {
		BlobsRemoved         int   `json:"blobs_removed"`
		BytesFreed           int64 `json:"bytes_freed"`
		FUSEMountsKilled     int   `json:"fuse_mounts_killed"`
		BuildCacheRemoved    int   `json:"build_cache_removed"`
		BuildCacheBytesFreed int64 `json:"build_cache_bytes_freed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		fmt.Println("Failed to decode response:", err)
		return
	}

	utils.PrintSuccess("System Prune Completed:")
	fmt.Printf("  - Active FUSE mounts killed: %d\n", report.FUSEMountsKilled)
	fmt.Printf("  - Orphaned blobs removed:   %d\n", report.BlobsRemoved)
	fmt.Printf("  - Disk space recovered:     %s\n", formatBytes(uint64(report.BytesFreed)))
	if report.BuildCacheRemoved > 0 || report.BuildCacheBytesFreed > 0 {
		fmt.Printf("  - Build cache entries removed: %d\n", report.BuildCacheRemoved)
		fmt.Printf("  - Build cache recovered:       %s\n", formatBytes(uint64(report.BuildCacheBytesFreed)))
	}
}

func execCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox exec [-it] <containerID> <cmd...>")
	}

	i := 2
	interactive := false
	if os.Args[i] == "-it" || os.Args[i] == "-ti" {
		interactive = true
		i++
	}
	if len(os.Args) <= i {
		exitf(2, "Usage: minibox exec [-it] <containerID> <cmd...>")
	}
	id := os.Args[i]
	cmdArgs := os.Args[i+1:]

	if len(cmdArgs) == 0 {
		cmdArgs = []string{"/bin/sh"}
	}

	execWithArgs(id, cmdArgs, interactive)
}

func composeCommand() {
	if len(os.Args) < 3 {
		exitf(2, "Usage: minibox compose [up|down|ps|logs|build|start|stop|restart] [-f file]")
	}

	action := os.Args[2]
	filename := "minibox-compose.yaml"
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "-f" && i+1 < len(os.Args) {
			filename = os.Args[i+1]
			break
		}
	}

	conf, err := compose.ParseConfig(filename)
	if err != nil {
		exitf(1, "Failed to parse compose file: %v", err)
	}

	projectName := conf.Name
	if projectName == "" {
		abs, _ := filepath.Abs(".")
		projectName = filepath.Base(abs)
	}

	switch action {
	case "up":
		composeUp(conf, projectName, filename)
	case "down":
		composeDown(projectName)
	case "ps":
		composePs(projectName)
	case "logs":
		composeLogs(projectName)
	case "build":
		composeBuild(conf, projectName, filename)
	case "start":
		composeUp(conf, projectName, filename) // For now start and up are similar
	case "stop":
		composeLifecycleAction(projectName, "stop")
	case "restart":
		composeRestart(conf, projectName, filename)
	default:
		exitf(2, "Unknown compose action: %s", action)
	}
}

func getProjectContainers(projectName string) map[string]map[string]any {
	resp, err := apiGET("/containers")
	if err != nil {
		exitf(1, "Failed to list containers: %v", err)
	}
	defer resp.Body.Close()
	var all map[string]map[string]any
	json.NewDecoder(resp.Body).Decode(&all)

	projectContainers := make(map[string]map[string]any)
	for id, c := range all {
		if proj, ok := c["project"].(string); ok && proj == projectName {
			projectContainers[id] = c
		}
	}
	return projectContainers
}

func composeUp(conf *models.ComposeConfig, projectName, filename string) {
	sorted, err := compose.SortServices(conf)
	if err != nil {
		exitf(1, "Dependency sort failed: %v", err)
	}

	for _, name := range sorted {
		svc := conf.Services[name]
		utils.PrintInfo("Preparing service: %s", name)

		imageName := svc.Image
		if svc.Build != "" {
			if imageName == "" {
				imageName = fmt.Sprintf("%s-%s", projectName, name)
			}
			buildDir := svc.Build
			if !filepath.IsAbs(buildDir) {
				composeDir := filepath.Dir(filename)
				buildDir = filepath.Join(composeDir, buildDir)
			}
			utils.PrintInfo("Building image for %s...", name)
			if err := triggerBuild(imageName, buildDir); err != nil {
				exitf(1, "Build failed for service %s: %v", name, err)
			}
		}

		if imageName == "" {
			exitf(1, "Service %s has no image or build context", name)
		}

		portMap := map[string]string{}
		for _, p := range svc.Ports {
			parts := strings.SplitN(p, ":", 2)
			if len(parts) == 2 {
				portMap[parts[0]] = parts[1]
			}
		}

		volumeMap := map[string]string{}
		for _, v := range svc.Volumes {
			parts := strings.SplitN(v, ":", 2)
			if len(parts) == 2 {
				absHost, _ := filepath.Abs(parts[0])
				volumeMap[absHost] = parts[1]
			}
		}

		namedVolumes := map[string]string{}
		if svc.DataPath != "" {
			vname := fmt.Sprintf("%s-%s-data", projectName, name)
			namedVolumes[vname] = svc.DataPath
		}

		shmSize := svc.ShmSize
		oomScore := svc.OOMScoreAdj
		if svc.DBMode {
			if shmSize == 0 { shmSize = 256 }
			if oomScore == 0 { oomScore = -900 }
		}

		reqBody := map[string]any{
			"image":         imageName,
			"command":       svc.Command,
			"env":           svc.Environment,
			"ports":         portMap,
			"volumes":       volumeMap,
			"named_volumes": namedVolumes,
			"name":          name,
			"project":       projectName,
			"detached":      true,
			"db_mode":       svc.DBMode,
			"shm_size":      shmSize,
			"user":          svc.User,
			"oom_score_adj": oomScore,
		}

		jsonData, _ := json.Marshal(reqBody)
		resp, err := apiPOST("/containers/run", "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			exitf(1, "Request failed for service %s: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			exitf(1, "Service %s failed to start: %s", name, readHTTPError(resp))
		}
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Service %s started: %s\n", name, string(body))
	}
	utils.PrintInfo("Project %s is up.", projectName)
}

func composeDown(projectName string) {
	containers := getProjectContainers(projectName)
	if len(containers) == 0 {
		utils.PrintInfo("No services found for project: %s", projectName)
		return
	}

	for id := range containers {
		utils.PrintInfo("Stopping container %s...", id)
		apiPOST("/containers/stop?id="+id, "application/json", nil)

		utils.PrintInfo("Removing container %s...", id)
		apiPOST("/containers/remove?id="+id, "application/json", nil)
	}
	utils.PrintInfo("Project %s down.", projectName)
}

func composePs(projectName string) {
	containers := getProjectContainers(projectName)
	
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, utils.ColorBold+"SERVICE\tCONTAINER ID\tIMAGE\tSTATUS\tPORTS"+utils.ColorReset)
	
	for id, c := range containers {
		name, _ := c["name"].(string)
		img, _ := c["image"].(string)
		status, _ := c["status"].(string)
		
		ports := "-"
		if pm, ok := c["ports"].(map[string]any); ok && len(pm) > 0 {
			var pairs []string
			for hp, cp := range pm {
				pairs = append(pairs, fmt.Sprintf("0.0.0.0:%s->%s/tcp", hp, cp))
			}
			ports = strings.Join(pairs, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, id, img, status, ports)
	}
	w.Flush()
	if len(containers) == 0 {
		fmt.Printf("No services found for project: %s\n", projectName)
	}
}

func composeLogs(projectName string) {
	containers := getProjectContainers(projectName)
	if len(containers) == 0 {
		exitf(1, "No services found for project: %s", projectName)
	}

	var wg sync.WaitGroup
	colors := []string{utils.ColorCyan, utils.ColorYellow, utils.ColorGreen, utils.ColorPurple, utils.ColorBlue, utils.ColorRed}
	
	idx := 0
	for id, c := range containers {
		name, _ := c["name"].(string)
		color := colors[idx % len(colors)]
		idx++

		wg.Add(1)
		go func(cid, cname, col string) {
			defer wg.Done()
			resp, err := apiGET("/containers/logs?id=" + url.QueryEscape(cid) + "&follow=1")
			if err != nil {
				fmt.Printf("[%s] Error fetching logs: %v\n", cname, err)
				return
			}
			defer resp.Body.Close()

			reader := bufio.NewReader(resp.Body)
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					fmt.Printf("%s%s |%s %s", col, cname, utils.ColorReset, line)
				}
				if err != nil {
					break
				}
			}
		}(id, name, color)
	}
	wg.Wait()
}

func composeBuild(conf *models.ComposeConfig, projectName, filename string) {
	for name, svc := range conf.Services {
		if svc.Build == "" {
			continue
		}
		imageName := svc.Image
		if imageName == "" {
			imageName = fmt.Sprintf("%s-%s", projectName, name)
		}
		buildDir := svc.Build
		if !filepath.IsAbs(buildDir) {
			composeDir := filepath.Dir(filename)
			buildDir = filepath.Join(composeDir, buildDir)
		}
		utils.PrintInfo("Building service: %s", name)
		if err := triggerBuild(imageName, buildDir); err != nil {
			exitf(1, "Build failed for service %s: %v", name, err)
		}
	}
}

func composeLifecycleAction(projectName string, action string) {
	containers := getProjectContainers(projectName)
	if len(containers) == 0 {
		exitf(1, "No services found for project: %s", projectName)
	}

	for id, c := range containers {
		name, _ := c["name"].(string)
		utils.PrintInfo("%s service: %s (%s)", strings.Title(action), name, id)
		
		switch action {
		case "stop":
			apiPOST("/containers/stop?id="+url.QueryEscape(id), "application/json", nil)
		case "restart":
			// Stop then re-run
			apiPOST("/containers/stop?id="+url.QueryEscape(id), "application/json", nil)
			apiPOST("/containers/remove?id="+url.QueryEscape(id), "application/json", nil)
			// restart handled by composeRestart
		}
	}
}

func composeRestart(conf *models.ComposeConfig, projectName, filename string) {
	// For each service in the project, stop it if it exists, then up it.
	composeDown(projectName)
	composeUp(conf, projectName, filename)
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
		fmt.Printf("minibox %s\n", version.Version)
		return
	}

	switch command {
	case "ping":
		ping()
	case "build":
		buildCommand()
	case "run":
		runCommand()
	case "db":
		dbCommand()
	case "ps":
		psCommand()
	case "stats":
		statsCommand()
	case "logs":
		logsCommand()
	case "exec":
		execCommand()
	case "compose":
		composeCommand()

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
