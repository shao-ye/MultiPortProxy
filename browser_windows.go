package main

import (
	"os"
	"path/filepath"
)

const browserDefault = "default"

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
		return browserDefault
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

func findInstalledBrowser(choice string) (browserInfo, bool) {
	choice = normalizeBrowserChoice(choice)
	if choice == browserDefault {
		return browserInfo{}, false
	}
	for _, browser := range detectInstalledBrowsers() {
		if browser.ID == choice {
			return browser, true
		}
	}
	return browserInfo{}, false
}

func browserArgs(browser browserInfo, url string) []string {
	if browser.AppMode {
		return []string{"--app=" + url}
	}
	return []string{url}
}
