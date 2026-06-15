package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// browserSystem 表示"跟随系统默认浏览器"。系统默认若不支持独立应用窗口，
// 会自动回退到第一个支持应用窗口的浏览器（见 findInstalledBrowser）。
const browserSystem = "system"

type browserInfo struct {
	ID      string
	Name    string
	ExeName string
	Path    string
	AppMode bool
	paths   []string
}

func normalizeBrowserChoice(choice string) string {
	switch choice {
	case "edge", "chrome", "firefox", "brave", "vivaldi", "opera":
		return choice
	default:
		// 空串、旧的 "auto"、未知值统一按"跟随系统默认"处理
		return browserSystem
	}
}

func knownBrowsers() []browserInfo {
	localAppData := os.Getenv("LOCALAPPDATA")
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	exePath := func(root string, elems ...string) string {
		if root == "" {
			return ""
		}
		return filepath.Join(append([]string{root}, elems...)...)
	}

	return []browserInfo{
		{
			ID:      "edge",
			Name:    "Microsoft Edge",
			ExeName: "msedge.exe",
			AppMode: true,
			paths: []string{
				exePath(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe"),
				exePath(programFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
				exePath(localAppData, "Microsoft", "Edge", "Application", "msedge.exe"),
			},
		},
		{
			ID:      "chrome",
			Name:    "Google Chrome",
			ExeName: "chrome.exe",
			AppMode: true,
			paths: []string{
				exePath(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
				exePath(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
				exePath(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
			},
		},
		{
			ID:      "firefox",
			Name:    "Mozilla Firefox",
			ExeName: "firefox.exe",
			AppMode: false,
			paths: []string{
				exePath(programFiles, "Mozilla Firefox", "firefox.exe"),
				exePath(programFilesX86, "Mozilla Firefox", "firefox.exe"),
				exePath(localAppData, "Mozilla Firefox", "firefox.exe"),
			},
		},
		{
			ID:      "brave",
			Name:    "Brave",
			ExeName: "brave.exe",
			AppMode: true,
			paths: []string{
				exePath(programFiles, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				exePath(programFilesX86, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				exePath(localAppData, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			},
		},
		{
			ID:      "vivaldi",
			Name:    "Vivaldi",
			ExeName: "vivaldi.exe",
			AppMode: true,
			paths: []string{
				exePath(programFiles, "Vivaldi", "Application", "vivaldi.exe"),
				exePath(programFilesX86, "Vivaldi", "Application", "vivaldi.exe"),
				exePath(localAppData, "Vivaldi", "Application", "vivaldi.exe"),
			},
		},
		{
			ID:      "opera",
			Name:    "Opera",
			ExeName: "opera.exe",
			AppMode: true,
			paths: []string{
				exePath(localAppData, "Programs", "Opera", "opera.exe"),
				exePath(localAppData, "Programs", "Opera GX", "opera.exe"),
				exePath(programFiles, "Opera", "opera.exe"),
				exePath(programFilesX86, "Opera", "opera.exe"),
			},
		},
	}
}

func detectInstalledBrowsers() []browserInfo {
	var installed []browserInfo
	for _, browser := range knownBrowsers() {
		for _, path := range browser.paths {
			if path == "" {
				continue
			}
			if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
				browser.Path = path
				browser.paths = nil
				installed = append(installed, browser)
				break
			}
		}
	}
	return installed
}

func appModeBrowsers(browsers []browserInfo) []browserInfo {
	var appBrowsers []browserInfo
	for _, browser := range browsers {
		if browser.AppMode {
			appBrowsers = append(appBrowsers, browser)
		}
	}
	return appBrowsers
}

func findInstalledBrowser(choice string) (browserInfo, bool) {
	choice = normalizeBrowserChoice(choice)
	browsers := detectInstalledBrowsers()
	switch choice {
	case browserSystem:
		if browser, ok := systemDefaultBrowser(browsers); ok && browser.AppMode {
			return browser, true
		}
		return firstAppModeBrowser(browsers) // 系统默认不支持应用窗口时回退
	default:
		for _, browser := range browsers {
			if browser.ID == choice {
				if browser.AppMode {
					return browser, true
				}
				return firstAppModeBrowser(browsers)
			}
		}
		return browserInfo{}, false
	}
}

func firstAppModeBrowser(browsers []browserInfo) (browserInfo, bool) {
	for _, browser := range browsers {
		if browser.AppMode {
			return browser, true
		}
	}
	return browserInfo{}, false
}

func systemDefaultBrowser(browsers []browserInfo) (browserInfo, bool) {
	id := detectSystemDefaultBrowserID()
	if id == "" {
		return browserInfo{}, false
	}
	for _, browser := range browsers {
		if browser.ID == id {
			return browser, true
		}
	}
	return browserInfo{}, false
}

func systemDefaultAppModeBrowser(browsers []browserInfo) (browserInfo, bool) {
	browser, ok := systemDefaultBrowser(browsers)
	if !ok || !browser.AppMode {
		return browserInfo{}, false
	}
	return browser, true
}

func detectSystemDefaultBrowserID() string {
	for _, scheme := range []string{"https", "http"} {
		key := `HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\` + scheme + `\UserChoice`
		cmd := exec.Command("reg", "query", key, "/v", "ProgId")
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if id := browserIDFromProgID(string(out)); id != "" {
			return id
		}
	}
	return ""
}

func browserIDFromProgID(output string) string {
	fields := strings.Fields(output)
	for i, field := range fields {
		if strings.EqualFold(field, "REG_SZ") && i+1 < len(fields) {
			progID := strings.ToLower(fields[i+1])
			switch {
			case strings.Contains(progID, "msedge") || strings.Contains(progID, "microsoftedge"):
				return "edge"
			case strings.Contains(progID, "chrome"):
				return "chrome"
			case strings.Contains(progID, "brave"):
				return "brave"
			case strings.Contains(progID, "vivaldi"):
				return "vivaldi"
			case strings.Contains(progID, "opera"):
				return "opera"
			case strings.Contains(progID, "firefox"):
				return "firefox"
			}
		}
	}
	return ""
}

func browserArgs(browser browserInfo, url string) []string {
	if browser.AppMode {
		return []string{"--app=" + url}
	}
	if browser.ExeName == "firefox.exe" {
		return []string{"--new-window", url}
	}
	return []string{url}
}
