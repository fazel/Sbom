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
	ReleaseNotesList []string
	Status           string
}

type NpmPackageJSON struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
	//DevDependencies map[string]string `json:"devDependencies"`
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
		return github.NewClient(nil)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

// parsePackageJSON: حالا فایل را می‌خواند
func parsePackageJSON(filename string) (NpmPackageJSON, error) {
	var pkgJSON NpmPackageJSON

	// 1. خواندن کل محتوای فایل
	data, err := os.ReadFile(filename)
	if err != nil {
		return pkgJSON, fmt.Errorf("error reading %s: %w", filename, err)
	}

	// 2. Unmarshal کردن
	err = json.Unmarshal(data, &pkgJSON)
	if err != nil {
		return pkgJSON, fmt.Errorf("error unmarshalling package.json: %w", err)
	}
	return pkgJSON, nil
}

func parseGitHubRepoURL(url string) (owner, repo string) {
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

// --- Changelog Extraction Helpers ---

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

// --- Core Check Logic ---

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
		info.Status = "✅ Up to date"
		return info
	}

	info.UpdateNeeded = true

	repoURL := npmInfo.Repository.URL
	owner, repo := parseGitHubRepoURL(repoURL)

	if owner != "" && repo != "" {

		release, resp, tagErr := client.Repositories.GetLatestRelease(context.Background(), owner, repo)

		if resp != nil && resp.StatusCode == http.StatusForbidden && strings.Contains(resp.Header.Get("X-RateLimit-Remaining"), "0") {

			resetTimeString := resp.Header.Get("X-RateLimit-Reset")
			resetTimeInt, err := strconv.ParseInt(resetTimeString, 10, 64)

			if err == nil {
				resetTime := time.Unix(resetTimeInt, 0)
				info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("❌ GitHub Rate Limit Exceeded. Try again after %s. (Repo: %s/%s)", resetTime.Format(time.RFC1123), owner, repo))
			} else {
				info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("❌ GitHub Rate Limit Exceeded. (Error parsing time: %v) (Repo: %s/%s)", err, owner, repo))
			}

		} else if release != nil && tagErr == nil {

			githubTag := release.GetTagName()

			cleanTagParts := strings.Split(githubTag, "@")
			if len(cleanTagParts) > 1 {
				githubTag = cleanTagParts[len(cleanTagParts)-1]
			}
			if !strings.HasPrefix(githubTag, "v") {
				githubTag = "v" + githubTag
			}

			if semver.Compare(info.CurrentVersion, githubTag) < 0 {

				body := strings.ToLower(release.GetBody() + " " + release.GetName())
				if strings.Contains(body, "security") || strings.Contains(body, "vulnerability") || strings.Contains(body, "cve") || strings.Contains(body, "patch") {
					info.SecurityPatch = true
				}

				releaseDetail := fmt.Sprintf("\n--- Latest Changelog for %s (Tag: %s) (Owner: %s) (Repo: %s) ---\n%s\n", release.GetName(), release.GetTagName(), owner, repo, release.GetBody())
				info.ReleaseNotesList = append(info.ReleaseNotesList, releaseDetail)
			}

		} else if tagErr != nil && strings.Contains(tagErr.Error(), "404 Not Found") {

			tags, _, tagListErr := client.Repositories.ListTags(context.Background(), owner, repo, &github.ListOptions{PerPage: 10})

			if tagListErr == nil && len(tags) > 0 {
				for _, tag := range tags {
					if strings.HasSuffix(tag.GetName(), info.LatestVersion[1:]) || tag.GetName() == info.LatestVersion {
						info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Found Tag '%s'. Changelog unavailable (Repo: %s/%s)", tag.GetName(), owner, repo))
						break
					}
				}
			}

			if len(info.ReleaseNotesList) == 0 {
				info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Could not fetch release or tag details from GitHub (%s/%s). Error: %v", owner, repo, tagErr))
			}

		} else if tagErr != nil {
			info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Could not fetch latest release details from GitHub (%s/%s). Error: %v", owner, repo, tagErr))
		}
	} else {
		info.ReleaseNotesList = append(info.ReleaseNotesList, fmt.Sprintf("Warning: Could not extract GitHub repo from NPM URL: %s", repoURL))
	}

	if info.SecurityPatch {
		info.Status = "🚨 URGENT Update Required (Security Patch!)"
	} else if len(info.ReleaseNotesList) == 0 || strings.HasPrefix(info.ReleaseNotesList[0], "Warning:") || strings.HasPrefix(info.ReleaseNotesList[0], "❌") {
		info.Status = "🔄 Update Recommended (Changelog unavailable)"
	} else {
		info.Status = "🔄 Update Recommended"
	}

	return info
}

// --- Final Output Function (Table Only, New Header) ---

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
	_, _ = writer.WriteString(fmt.Sprintf("# 📈 گزارش وضعیت به‌روزرسانی وابستگی‌های فرانت‌اند\n\n"))
	_, _ = writer.WriteString(fmt.Sprintf("## پروژه‌ی **%s** (`%s`)\n", pkgJSON.Name, pkgJSON.Version))
	_, _ = writer.WriteString("این گزارش خلاصه‌ای از وضعیت به‌روزرسانی وابستگی‌های اصلی (`dependencies`) شما را نمایش می‌دهد.\n")
	_, _ = writer.WriteString("> **توجه:** 'نیاز به آپدیت' به معنای توصیه شدن آپدیت است، مگر آنکه پچ امنیتی ذکر شود.\n\n")
	_, _ = writer.WriteString("---\n\n")

	_, _ = writer.WriteString("## خلاصه وضعیت به‌روزرسانی\n\n")

	// Markdown Table Header (اضافه شدن ستون ایندکس)
	_, _ = writer.WriteString("| # | 📦 پکیج | 🟢 وضعیت | 🏷️ نسخه فعلی | ⬆️ آخرین نسخه NPM | 📝 چنج‌لاگ (خلاصه) |\n")
	_, _ = writer.WriteString("| :---: | :--- | :---: | :---: | :--- | :--- |\n")

	index := 1
	for _, info := range infos {
		// 1. تعیین نمایش وضعیت
		statusDisplay := info.Status
		if info.SecurityPatch {
			statusDisplay = "**🚨 پچ امنیتی!**"
		} else if info.UpdateNeeded && strings.Contains(info.Status, "Changelog unavailable") {
			statusDisplay = "🔄 نیاز به آپدیت (نامشخص)"
		} else if info.UpdateNeeded {
			statusDisplay = "🔄 نیاز به آپدیت"
		} else {
			statusDisplay = "✅ به‌روز است"
		}

		// 2. استخراج لینک و چنج‌لاگ
		repoLinkURL := ""
		changelogSummary := "N/A"

		if len(info.ReleaseNotesList) > 0 {
			notes := info.ReleaseNotesList[0]

			// الف) خطا/عدم دسترسی یا فقط تگ پیدا شده است
			if strings.HasPrefix(notes, "❌") || strings.HasPrefix(notes, "Warning:") {
				if owner, repo := getOwnerRepoFromError(notes); owner != "" {
					repoLinkURL = fmt.Sprintf("https://github.com/%s/%s/tags", owner, repo)
					changelogSummary = strings.Split(notes, "(Repo:")[0]
					changelogSummary = strings.TrimPrefix(changelogSummary, "Warning: ")
					changelogSummary = strings.TrimPrefix(changelogSummary, "❌ ")
				} else {
					changelogSummary = "❌ خطا: دسترسی به GitHub"
				}
			} else {
				// ب) استخراج اطلاعات از چنج‌لاگ موفق
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

				// خلاصه کردن چنج‌لاگ (۸۰ کاراکتر اول)
				changelogBody := extractBodyFromChangelog(notes)
				if len(changelogBody) > 80 {
					changelogSummary = strings.TrimSpace(changelogBody[:80]) + "..."
				} else {
					changelogSummary = strings.TrimSpace(changelogBody)
				}
			}
		}

		// 3. ساخت فرمت Markdown لینک برای آخرین نسخه (NPM)
		latestVersionDisplay := info.LatestVersion
		if repoLinkURL != "" {
			// لینک به صورت: [v7.3.5](URL)
			latestVersionDisplay = fmt.Sprintf("[`%s`](%s)", info.LatestVersion, repoLinkURL)
		}

		// 4. نوشتن ردیف جدول
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

	// 1. خواندن از فایل
	pkgJSON, err := parsePackageJSON(packageFileName)
	if err != nil {
		fmt.Printf("Fatal Error: Could not read or parse %s. %v\n", packageFileName, err)
		return
	}

	var results []UpdateInfo

	// فیلتر کردن وابستگی‌های محلی (مثل file:libs/...)
	// فقط dependencies اصلی را بررسی می‌کنیم
	packagesToCheck := pkgJSON.Dependencies

	filteredPackages := make(map[string]string)
	for pkgName, ver := range packagesToCheck {
		// ignore local file paths and complex git urls
		if !strings.HasPrefix(ver, "file:") && !strings.Contains(ver, "git") {
			filteredPackages[pkgName] = ver
		}
	}

	fmt.Printf("شروع بررسی %d پکیج (پروژه: %s@%s)...\n", len(filteredPackages), pkgJSON.Name, pkgJSON.Version)

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

	fmt.Printf("✅ عملیات با موفقیت انجام شد. نتایج در فایل **%s** ذخیره گردید.\n", outputFile)
}
