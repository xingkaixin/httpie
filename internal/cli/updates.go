package cli

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"httpie/internal/core"
)

type versionInfo struct {
	LastFetched string            `json:"last_fetched_date"`
	LastWarned  *string           `json:"last_warned_date"`
	Versions    map[string]string `json:"last_released_versions"`
}

func updateSettings() (string, bool) {
	var config struct {
		Disabled bool   `json:"disable_update_warnings"`
		File     string `json:"version_info_file"`
	}
	data, _ := os.ReadFile(filepath.Join(core.ConfigDir(), "config.json"))
	_ = json.Unmarshal(data, &config)
	if config.File == "" {
		config.File = filepath.Join(core.ConfigDir(), "version_info.json")
	}
	return core.ExpandPath(config.File), config.Disabled
}
func readVersionInfo(path string) versionInfo {
	var info versionInfo
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &info)
	return info
}
func fetchUpdates() error {
	path, _ := updateSettings()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The baseline's update index request uses requests.get(..., verify=False).
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport}
	response, err := client.Get("https://packages.httpie.io/latest.json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return fmt.Errorf("update check: %s", response.Status)
	}
	info := readVersionInfo(path)
	if err = json.NewDecoder(response.Body).Decode(&info.Versions); err != nil {
		return err
	}
	info.LastFetched = time.Now().Format("2006-01-02T15:04:05.999999")
	return saveVersionInfo(path, info)
}

func saveVersionInfo(path string, info versionInfo) error {
	fields := map[string]any{}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &fields)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["last_fetched_date"] = info.LastFetched
	fields["last_warned_date"] = info.LastWarned
	fields["last_released_versions"] = info.Versions
	return core.SaveJSON(path, fields)
}

func updateMessage(info versionInfo) string {
	latest := info.Versions["unknown"]
	if latest == "" || !core.NewerVersion(latest, core.Version) {
		return ""
	}
	return fmt.Sprintf("A new HTTPie release (%s) is available.\nTo see how you can update, please visit https://httpie.io/docs/cli/unknown\n", latest)
}
func updateStatus() string {
	path, _ := updateSettings()
	if message := updateMessage(readVersionInfo(path)); message != "" {
		return message
	}
	return "You are already up-to-date.\n"
}
func checkUpdateWarnings(stderr io.Writer, program string, quiet bool) {
	path, disabled := updateSettings()
	if disabled {
		return
	}
	info := readVersionInfo(path)
	parseTime := func(value string) time.Time {
		date, _ := time.ParseInLocation("2006-01-02T15:04:05.999999", value, time.Local)
		return date
	}
	if message := updateMessage(info); message != "" && (info.LastWarned == nil || time.Since(parseTime(*info.LastWarned)) > 7*24*time.Hour) {
		if !quiet {
			fmt.Fprintf(stderr, "\n%s: info: %s\n", program, message)
		}
		now := time.Now().Format("2006-01-02T15:04:05.999999")
		info.LastWarned = &now
		_ = saveVersionInfo(path, info)
	}
	if time.Since(parseTime(info.LastFetched)) < 14*24*time.Hour {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	lockPath := path + ".lock"
	if stat, err := os.Stat(lockPath); err == nil && time.Since(stat.ModTime()) > time.Minute {
		_ = os.Remove(lockPath)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	lock.Close()
	executable, err := os.Executable()
	if err != nil {
		os.Remove(lockPath)
		return
	}
	child := exec.Command(executable, "--internal-check-updates")
	if err = child.Start(); err != nil {
		os.Remove(lockPath)
		return
	}
	_ = child.Process.Release()
}
