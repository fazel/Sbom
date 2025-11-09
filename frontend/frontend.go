package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/semver"
	"golang.org/x/oauth2"
)

// --- Data Structures ---

type UpdateInfo struct {
	Repo             string
	CurrentVersion   string
	LatestVersion    string
	UpdateNeeded     bool
	SecurityPatch    bool
	IsArchived       bool // NEW: To track if the repository is archived (deprecated)
	ReleaseNotesList []string
	Status           string
}

type NpmPackageJSON struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type NpmInfo struct {
	Version    string `json:"version"`
	Repository struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"repository"`
}

// --- Utility Functions ---

func createGitHubClient() *github.Client {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		// Fallback for unauthenticated client
		return github.NewClient(nil)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

func parsePackageJSON(filename string) (NpmPackageJSON, error) {
	var pkgJSON NpmPackageJSON
	data, err := os.ReadFile(filename)
	if err != nil {
		return pkgJSON, fmt.Errorf("error reading %s: %w", filename, err)
	}
	err = json.Unmarshal(data, &pkgJSON)
	if err != nil {
		return pkgJSON, fmt.Errorf("error unmarshalling package.json: %w", err)
	}
	return pkgJSON, nil
}

func parseGitHubRepoURL(url string) (owner, repo string) {
	// Aggressive cleaning logic (Unchanged)
	url = strings.TrimPrefix(url, "git://")
	url = strings.TrimPrefix(url, "git+https://")
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "git@")

	url = strings.Split(url, "#")[0]
	url = strings.TrimSuffix(url, ".git")

	if parts := strings.Split(url, ":"); len(parts) > 1 {
		url = parts[1]
	}

	parts := strings.Split(url, "/")
	var filteredParts []string
	for _, p := range parts {
		if p != "" {
			filteredParts = append(filteredParts, p)
		}
	}
	parts = filteredParts

	if len(parts) >= 2 && (strings.Contains(parts[0], "github.com") || strings.Contains(parts[0], "gitlab.com")) {
		if len(parts) >= 3 {
			return parts[1], parts[2]
		}
	} else if len(parts) >= 2 {
		return parts[0], parts[1]
	}

	return "", ""
}

func fetchNpmInfo(pkgName string) (*NpmInfo, error) {
	url := fmt.Sprintf("https://registry.npmjs.org/%s/latest", pkgName)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm API returned status %d for package %s", resp.StatusCode, pkgName)
	}

	var info NpmInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// --- Changelog & Error Extraction Helpers (Unchanged) ---

func getOwnerRepoFromChangelog(notes string) (owner, repo string) {
	if ownerIndex := strings.Index(notes, "(Owner: "); ownerIndex != -1 {
		ownerEnd := strings.Index(notes[ownerIndex+8:], ")")
		if ownerEnd != -1 {
			owner = notes[ownerIndex+8 : ownerIndex+8+ownerEnd]
		}
	}
	if repoIndex := strings.Index(notes, "(Repo: "); repoIndex != -1 {
		repoEnd := strings.Index(notes[repoIndex+7:], ")")
		if repoEnd != -1 {
			repo = notes[repoIndex+7 : repoIndex+7+repoEnd]
		}
	}
	return
}

func getOwnerRepoFromError(notes string) (owner, repo string) {
	if strings.Contains(notes, "GitHub (") {
		start := strings.Index(notes, "GitHub (") + 8
		end := strings.Index(notes, ")")
		if end > start {
			parts := strings.Split(notes[start:end], "/")
			if len(parts) >= 2 {
				repoWithSuffix := strings.Split(parts[1], ".git")[0]
				return parts[0], repoWithSuffix
			}
		}
	}
	return "", ""
}

func extractBodyFromChangelog(notes string) string {
	body := strings.Split(notes, "---")[2]
	body = strings.TrimSpace(body)

	body = strings.ReplaceAll(body, "*", "")
	body = strings.ReplaceAll(body, "**", "")
	body = strings.ReplaceAll(body, "#", "")
	body = strings.ReplaceAll(body, "[", "")
	body = strings.ReplaceAll(body, "]", "")
	body = strings.ReplaceAll(body, "(", "")
	body = strings.ReplaceAll(body, ")", "")
	body = strings.ReplaceAll(body, "`", "")

	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.ReplaceAll(body, "\r", " ")

	for strings.Contains(body, "  ") {
		body = strings.ReplaceAll(body, "  ", " ")
	}

	body = strings.ReplaceAll(body, "|", "\\|")

	return body
}

// --- Core Check Logic (Updated to include Archival Check) ---

func checkNpmUpdate(client *github.Client, pkgName, currentVer string) UpdateInfo {
	cleanVer := strings.TrimFunc(currentVer, func(r rune) bool {
		return strings.ContainsRune("^~=>", r)
	})
	if !strings.HasPrefix(cleanVer, "v") {
		cleanVer = "v" + cleanVer
	}

	info := UpdateInfo{
		Repo:           pkgName,
		CurrentVersion: cleanVer,
		LatestVersion:  "N/A",
	}

	npmInfo, err := fetchNpmInfo(pkgName)
	if err != nil {
		info.Status = "❌ NPM Fetch Error: " + err.Error()
		return info
	}

	latestVer := npmInfo.Version
	if !strings.HasPrefix(latestVer, "v") {
		latestVer = "v" + latestVer
	}
	info.LatestVersion = latestVer

	if semver.Compare(info.CurrentVersion, info.LatestVersion) >= 0 {
		info.UpdateNeeded = false // Explicitly set to false if up-to-date
	} else {
		info.UpdateNeeded = true
	}

	repoURL := npmInfo.Repository.URL
	owner, repo := parseGitHubRepoURL(repoURL)

	if owner == "" || repo == "" {
		info.Status = "🔄 Update Recommended (Repo link missing)"
		return info
	}

	// --- 1. CHECK ARCHIVED (DEPRECATED) STATUS ---
	repoDetails, _, repoErr := client.Repositories.Get(context.Background(), owner, repo)
	if repoErr != nil {
		fmt.Printf(" [ERROR] Could not fetch repo details for %s/%s: %v\n", owner, repo, repoErr)
	} else if repoDetails.GetArchived() {
		info.IsArchived = true
		info.Status = "⛔️ DEPRECATED (Archived)"
		fmt.Printf(" [DEPRECATED] Repository %s/%s is ARCHIVED.\n", owner, repo)
	}

	// If archived, no further version checks are strictly necessary, but we continue
	// to populate version info if UpdateNeeded is true.
	if info.IsArchived && !info.UpdateNeeded {
		return info // If archived AND up-to-date, stop here.
	}

	// --- 2. VERSION & SECURITY CHECK (Only if UpdateNeeded) ---
	if info.UpdateNeeded {

		releases, resp, listErr := client.Repositories.ListReleases(context.Background(), owner, repo, &github.ListOptions{
			PerPage: 30,
		})

		if resp != nil && resp.StatusCode == http.StatusForbidden && strings.Contains(resp.Header.Get("X-RateLimit-Remaining"), "0") {
			resetTimeString := resp.Header.Get("X-RateLimit-Reset")
			resetTimeInt, _ := strconv.ParseInt(resetTimeString, 10, 64)
			info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("❌ GitHub Rate Limit Exceeded. Try again after %s. (Repo: %s/%s)", time.Unix(resetTimeInt, 0).Format(time.RFC1123), owner, repo))

		} else if listErr != nil {
			info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Could not list releases from GitHub (%s/%s). Error: %v", owner, repo, listErr))

		} else {
			foundLatestReleaseChangelog := false

			for _, release := range releases {
				tag := release.GetTagName()

				cleanTagParts := strings.Split(tag, "@")
				if len(cleanTagParts) > 1 {
					tag = cleanTagParts[len(cleanTagParts)-1]
				}
				if !strings.HasPrefix(tag, "v") {
					tag = "v" + tag
				}

				if semver.Compare(tag, info.CurrentVersion) <= 0 {
					break
				}

				// Security Check (Checks all intermediate versions)
				body := strings.ToLower(release.GetBody() + " " + release.GetName())
				if strings.Contains(body, "security") || strings.Contains(body, "vulnerability") || strings.Contains(body, "cve") || strings.Contains(body, "patch") {
					info.SecurityPatch = true
				}

				// Store Changelog for the very latest version only
				if !foundLatestReleaseChangelog {
					releaseDetail := fmt.Sprintf("\n--- Latest Changelog for %s (Tag: %s) (Owner: %s) (Repo: %s) ---\n%s\n", release.GetName(), release.GetTagName(), owner, repo, release.GetBody())
					info.ReleaseNotesList = append(info.ReleaseNotesList, releaseDetail)
					foundLatestReleaseChangelog = true
				}
			}

			if !foundLatestReleaseChangelog {
				info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Could not fetch specific release details for version %s, or only tags exist. (Repo: %s/%s)", info.LatestVersion, owner, repo))
			}
		}

	}

	// 3. Final Status Assignment
	if info.IsArchived {
		if info.UpdateNeeded {
			info.Status = "⛔️ DEPRECATED (Update Needed)"
		} else {
			info.Status = "⛔️ DEPRECATED (Up to date)"
		}
	} else if info.SecurityPatch {
		info.Status = "🚨 URGENT Update Required (Security Patch!)"
	} else if !info.UpdateNeeded {
		info.Status = "✅ Up to date"
	} else if len(info.ReleaseNotesList) == 0 || strings.HasPrefix(info.ReleaseNotesList[0], "Warning:") || strings.HasPrefix(info.ReleaseNotesList[0], "❌") {
		info.Status = "🔄 Update Recommended (Changelog unavailable)"
	} else {
		info.Status = "🔄 Update Recommended"
	}

	return info
}

// --- Output Function (Markdown Table) ---

func writeOutput(pkgJSON NpmPackageJSON, infos []UpdateInfo, filename string) error {
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating output file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	// 1. Project Info Header
	_, _ = writer.WriteString(fmt.Sprintf("# 📈 Frontend Dependency Update Report\n\n"))
	_, _ = writer.WriteString(fmt.Sprintf("## Project: **%s** (`%s`)\n", pkgJSON.Name, pkgJSON.Version))
	_, _ = writer.WriteString("This report summarizes the update status for your main dependencies (`dependencies`).\n")
	_, _ = writer.WriteString("> **Note:** 'Update Recommended' means updating is advised, unless a security patch is explicitly noted.\n\n")
	_, _ = writer.WriteString("---\n\n")

	_, _ = writer.WriteString("## Summary of Update Status\n\n")

	// Markdown Table Header
	_, _ = writer.WriteString("| # | 📦 Package | 🟢 Status | 🏷️ Current Version | ⬆️ Latest Version | 📝 Changelog Summary |\n")
	_, _ = writer.WriteString("| :---: | :--- | :---: | :---: | :---: | :--- |\n")

	index := 1
	for _, info := range infos {
		// 1. Determine Status Display
		statusDisplay := info.Status

		// 2. Extract Link and Changelog Summary
		repoLinkURL := ""
		changelogSummary := "N/A"

		if len(info.ReleaseNotesList) > 0 {
			notes := info.ReleaseNotesList[0]

			if strings.HasPrefix(notes, "❌") || strings.HasPrefix(notes, "Warning:") {
				if owner, repo := getOwnerRepoFromError(notes); owner != "" {
					repoLinkURL = fmt.Sprintf("https://github.com/%s/%s/tags", owner, repo)
					changelogSummary = strings.Split(notes, "(Repo:")[0]
					changelogSummary = strings.TrimPrefix(changelogSummary, "Warning: ")
					changelogSummary = strings.TrimPrefix(changelogSummary, "❌ ")
				} else {
					changelogSummary = "❌ Error: GitHub access"
				}
			} else {
				owner, repo := getOwnerRepoFromChangelog(notes)
				tagStart := strings.Index(notes, "(Tag:") + 6
				tagEnd := strings.Index(notes, ")")
				gitHubTag := ""
				if tagEnd > tagStart {
					gitHubTag = strings.TrimSpace(notes[tagStart:tagEnd])
				}

				if owner != "" {
					if gitHubTag != "" {
						repoLinkURL = fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", owner, repo, gitHubTag)
					} else {
						repoLinkURL = fmt.Sprintf("https://github.com/%s/%s", owner, repo)
					}
				}

				changelogBody := extractBodyFromChangelog(notes)
				if len(changelogBody) > 80 {
					changelogSummary = strings.TrimSpace(changelogBody[:80]) + "..."
				} else {
					changelogSummary = strings.TrimSpace(changelogBody)
				}
			}
		}

		// 3. Create Markdown link for Latest Version
		latestVersionDisplay := info.LatestVersion
		if repoLinkURL != "" {
			latestVersionDisplay = fmt.Sprintf("[`%s`](%s)", info.LatestVersion, repoLinkURL)
		}

		// 4. Write table row
		line := fmt.Sprintf("| %d | `%s` | %s | `%s` | %s | %s |\n",
			index, info.Repo, statusDisplay, info.CurrentVersion, latestVersionDisplay, changelogSummary)
		_, _ = writer.WriteString(line)
		index++
	}

	return nil
}

func main() {
	const packageFileName = "frontend/package.json"
	const outputFile = "frontend/report.md"

	client := createGitHubClient()

	// 1. Read from file
	pkgJSON, err := parsePackageJSON(packageFileName)
	if err != nil {
		fmt.Printf("Fatal Error: Could not read or parse %s. %v\n", packageFileName, err)
		return
	}

	var results []UpdateInfo

	// 2. Process only 'dependencies'
	packagesToCheck := pkgJSON.Dependencies

	filteredPackages := make(map[string]string)
	for pkgName, ver := range packagesToCheck {
		if !strings.HasPrefix(ver, "file:") && !strings.Contains(ver, "git") {
			filteredPackages[pkgName] = ver
		}
	}

	fmt.Printf("Starting check for %d packages (Project: %s@%s)...\n", len(filteredPackages), pkgJSON.Name, pkgJSON.Version)

	for pkgName, currentVer := range filteredPackages {

		fmt.Printf("-> Checking NPM package %s (Current: %s)...\n", pkgName, currentVer)
		info := checkNpmUpdate(client, pkgName, currentVer)
		results = append(results, info)
	}

	err = writeOutput(pkgJSON, results, outputFile)
	if err != nil {
		fmt.Printf("Fatal Error writing output: %v\n", err)
		return
	}

	fmt.Printf("✅ Operation completed successfully. Results saved in **%s**.\n", outputFile)
}
