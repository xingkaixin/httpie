package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	data, _ := os.ReadFile(filepath.Join(configDir(), "config.json"))
	_ = json.Unmarshal(data, &config)
	if config.File == "" {
		config.File = filepath.Join(configDir(), "version_info.json")
	}
	return expandPath(config.File), config.Disabled
}
func readVersionInfo(path string) versionInfo {
	var info versionInfo
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &info)
	return info
}
func fetchUpdates() error {
	path, _ := updateSettings()
	client := http.Client{Timeout: 15 * time.Second}
	response, err := client.Get("https://packages.httpie.io/latest.json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("update check: %s", response.Status)
	}
	info := readVersionInfo(path)
	if err = json.NewDecoder(response.Body).Decode(&info.Versions); err != nil {
		return err
	}
	info.LastFetched = time.Now().Format("2006-01-02T15:04:05.999999")
	return saveJSON(path, info)
}

var versionDigits = regexp.MustCompile(`\d+`)

func newerVersion(candidate, current string) bool {
	a, b := versionDigits.FindAllString(candidate, 3), versionDigits.FindAllString(current, 3)
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(a) {
			x, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			y, _ = strconv.Atoi(b[i])
		}
		if x != y {
			return x > y
		}
	}
	return !strings.Contains(candidate, "dev") && strings.Contains(current, "dev")
}
func updateMessage(info versionInfo) string {
	latest := info.Versions["pypi"]
	if latest == "" || !newerVersion(latest, version) {
		return ""
	}
	return fmt.Sprintf("A new HTTPie release (%s) is available.\nTo see how you can update, please visit https://httpie.io/docs/cli/pypi\n", latest)
}
func updateStatus() string {
	path, _ := updateSettings()
	if message := updateMessage(readVersionInfo(path)); message != "" {
		return message
	}
	return "You are already up-to-date.\n"
}
func checkUpdateWarnings(stderr io.Writer, quiet int) {
	path, disabled := updateSettings()
	if disabled {
		return
	}
	info := readVersionInfo(path)
	parseTime := func(value string) time.Time {
		date, _ := time.ParseInLocation("2006-01-02T15:04:05.999999", value, time.Local)
		return date
	}
	if message := updateMessage(info); message != "" && quiet == 0 && (info.LastWarned == nil || time.Since(parseTime(*info.LastWarned)) > 7*24*time.Hour) {
		fmt.Fprintf(stderr, "\nhttp: info: %s\n", message)
		now := time.Now().Format("2006-01-02T15:04:05.999999")
		info.LastWarned = &now
		_ = saveJSON(path, info)
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
