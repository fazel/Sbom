package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/semver"
	"golang.org/x/oauth2"
)

// UpdateInfo struct holds the update status and full changelog for each repository
type UpdateInfo struct {
	Repo             string
	CurrentVersion   string
	LatestVersion    string
	UpdateNeeded     bool
	SecurityPatch    bool
	ReleaseNotesList []string // List to hold full changelog/release notes text for newer versions
	Status           string
}

// createGitHubClient initializes the GitHub client, using a PAT if available.
func createGitHubClient() *github.Client {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("⚠️ Warning: GITHUB_TOKEN environment variable not set. Using unauthenticated client (Severe rate limits apply).")
		return github.NewClient(nil)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)

	return github.NewClient(tc)
}

// readRepos reads repository lines from the input file (format: owner/repo vX.Y.Z)
func readRepos(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("error opening input file: %w", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// parseLine splits the input line into Owner, Repo, and Current Version
func parseLine(line string) (owner, repo, currentVer string) {
	parts := strings.Fields(line)
	if len(parts) != 2 {
		return "", "", ""
	}

	repoAndOwner := parts[0]
	currentVer = parts[1]

	// Ensure the current version has a 'v' prefix for proper SemVer comparison
	if !strings.HasPrefix(currentVer, "v") {
		currentVer = "v" + currentVer
	}

	repoParts := strings.Split(repoAndOwner, "/")
	if len(repoParts) == 2 {
		owner = repoParts[0]
		repo = repoParts[1]
	}
	return
}

// checkUpdate checks for updates and security patches for a single repository
func checkUpdate(client *github.Client, owner, repo, currentVer string) UpdateInfo {
	info := UpdateInfo{
		Repo:           owner + "/" + repo,
		CurrentVersion: currentVer,
		LatestVersion:  "N/A",
	}

	// Fetch the list of latest releases
	releases, _, err := client.Repositories.ListReleases(context.Background(), owner, repo, &github.ListOptions{
		PerPage: 10, // Check up to 10 recent releases
	})

	if err != nil {
		info.Status = "❌ ERROR: " + err.Error()
		return info
	}

	if len(releases) == 0 {
		info.Status = "❌ ERROR: No releases found."
		return info
	}

	latestRelease := releases[0]
	latestVer := latestRelease.GetTagName()

	if !strings.HasPrefix(latestVer, "v") {
		latestVer = "v" + latestVer
	}
	info.LatestVersion = latestVer

	if semver.Compare(info.CurrentVersion, info.LatestVersion) < 0 {
		info.UpdateNeeded = true
	} else {
		info.Status = "✅ Up to date"
		return info
	}

	// Check Release Notes and Security Patches (for newer releases)
	for _, release := range releases {
		tag := release.GetTagName()
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}

		if semver.Compare(info.CurrentVersion, tag) < 0 {
			body := strings.ToLower(release.GetBody() + " " + release.GetName())

			// Security Patch keywords check
			if strings.Contains(body, "security") || strings.Contains(body, "vulnerability") || strings.Contains(body, "cve") || strings.Contains(body, "patch") {
				info.SecurityPatch = true
			}

			// ** Collect the full changelog body **
			releaseDetail := fmt.Sprintf("\n--- Changelog for %s (%s) ---\n%s\n", release.GetName(), release.GetTagName(), release.GetBody())
			info.ReleaseNotesList = append(info.ReleaseNotesList, releaseDetail)
		}
	}

	// Set final status
	if info.SecurityPatch {
		info.Status = "🚨 URGENT Update Required (Security Patch!)"
	} else {
		info.Status = "🔄 Update Recommended"
	}

	return info
}

// writeOutput writes the results to an output file in Markdown format
func writeOutput(infos []UpdateInfo, filename string) error {
	// Ensure the filename ends with .md
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating output file: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			fmt.Println("error closing output file:", err)
		}
	}(file)

	writer := bufio.NewWriter(file)
	defer func(writer *bufio.Writer) {
		err := writer.Flush()
		if err != nil {
			fmt.Println("error flushing output file:", err)
		}
	}(writer)

	// Title
	_, _ = writer.WriteString("# 📈 GitHub Dependency Update Report\n\n")
	_, _ = writer.WriteString("This report summarizes the update status for all checked repositories.\n\n")
	_, _ = writer.WriteString("---\n\n")

	for _, info := range infos {
		// Repository Heading
		_, _ = writer.WriteString(fmt.Sprintf("## 📦 %s\n\n", info.Repo))

		statusText := info.Status
		if info.SecurityPatch {
			statusText = "🚨 URGENT Security Patch!"
		} else if info.UpdateNeeded {
			statusText = "🔄 Update Recommended"
		} else {
			statusText = "✅ Up to date"
		}

		_, _ = writer.WriteString(fmt.Sprintf("* **Status:** **%s**\n", statusText))
		_, _ = writer.WriteString(fmt.Sprintf("* Current Version: `%s`\n", info.CurrentVersion))
		_, _ = writer.WriteString(fmt.Sprintf("* Latest Version: `%s`\n\n", info.LatestVersion))

		if info.UpdateNeeded {
			_, _ = writer.WriteString("### 📝 Full Changelog\n")
			_, _ = writer.WriteString("> The following releases are newer than your current version. Changelog is ordered from newest to oldest.\n\n")

			// Displaying the full changelog list in a markdown code block
			for _, notes := range info.ReleaseNotesList {
				_, _ = writer.WriteString("```markdown\n")
				_, _ = writer.WriteString(notes)
				_, _ = writer.WriteString("\n```\n\n")
			}
		}

		_, _ = writer.WriteString("---\n\n") // Separator
	}
	return nil
}

func main() {
	const inputFile = "input.txt"
	const outputFile = "output.md" // Output file set to Markdown

	client := createGitHubClient()

	// 1. Read input
	lines, err := readRepos(inputFile)
	if err != nil {
		fmt.Printf("Fatal Error: %v\n", err)
		return
	}

	var results []UpdateInfo

	// 2. Process each repository
	fmt.Printf("Starting check for %d repositories...\n", len(lines))
	for _, line := range lines {
		owner, repo, currentVer := parseLine(line)
		if owner == "" || repo == "" || currentVer == "" {
			fmt.Printf("⚠️ Format Error: Line '%s' skipped.\n", line)
			continue
		}

		fmt.Printf("-> Checking %s/%s (Current: %s)...\n", owner, repo, currentVer)
		info := checkUpdate(client, owner, repo, currentVer)
		results = append(results, info)
	}

	// 3. Write output
	err = writeOutput(results, outputFile)
	if err != nil {
		fmt.Printf("Fatal Error: %v\n", err)
		return
	}

	fmt.Printf("✅ Operation completed successfully. Results saved in **%s**.\n", outputFile)
}
