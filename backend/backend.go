package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/semver"
	"golang.org/x/oauth2"
)

// --- Data Structures ---

type DependencyInfo struct {
	Name           string
	CurrentVersion string
	RepoURL        string
	LatestVersion  string
	UpdateNeeded   bool
	Status         string
}

// --- GitHub Client Setup ---

func createGitHubClient() *github.Client {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("⚠️ Warning: GITHUB_TOKEN not set. Running unauthenticated (low rate limit).")
		return github.NewClient(nil)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

// --- File Reading and Parsing ---

func readConfigFile(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("error reading config file %s: %w", filename, err)
	}
	return string(data), nil
}

func parseErlangDeps(configContent string) ([]DependencyInfo, error) {
	var deps []DependencyInfo

	re := regexp.MustCompile(`{deps,\s*\[([\s\S]*?)\]}`)
	match := re.FindStringSubmatch(configContent)
	if len(match) < 2 {
		return nil, fmt.Errorf("could not find {deps, [...]} block in config")
	}
	depsListString := "[" + match[1] + "]"

	// Cleanup logic (specific to ejabberd's complex config)
	cleanList := strings.ReplaceAll(depsListString, "{if_var_true, tools,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, elixir,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, pam,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, redis,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, sip,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, zlib,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, mysql,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, pgsql,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, sqlite,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_var_true, stun,", "")
	cleanList = strings.ReplaceAll(cleanList, "{if_version_above, \"19\",", "")
	cleanList = strings.ReplaceAll(cleanList, "if_not_rebar3", "")
	cleanList = strings.ReplaceAll(cleanList, "if_rebar3", "")
	cleanList = strings.ReplaceAll(cleanList, "{tag: ", "{tag, ")
	cleanList = strings.ReplaceAll(cleanList, "}} % for R19 and below", "}}")

	// Regex targets the common git/tag structure: {App, ".*", {git, "URL", {tag, "VERSION"}}}
	reDep := regexp.MustCompile(`{([a-zA-Z0-9_@-]+),\s*".*?",\s*{git,\s*"(https://[^"]+)",\s*{tag,\s*"([^"]+)"}}}`)
	matches := reDep.FindAllStringSubmatch(cleanList, -1)

	if len(matches) == 0 {
		return nil, fmt.Errorf("no standard git/tag dependencies found after cleanup")
	}

	for _, match := range matches {
		if len(match) == 4 {
			deps = append(deps, DependencyInfo{
				Name:           match[1],
				RepoURL:        match[2],
				CurrentVersion: match[3],
			})
		}
	}

	return deps, nil
}

// parseGitHubURL: Extracts owner and repo from the Git URL
func parseGitHubURL(url string) (owner, repo string) {
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimPrefix(url, "git://")
	url = strings.TrimPrefix(url, "https://github.com/")

	parts := strings.Split(url, "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// findLatestVersion: Finds the latest version from GitHub tags/releases
func findLatestVersion(client *github.Client, owner, repo string) (string, error) {
	ctx := context.Background()
	latestValidVersion := ""

	// 1. Try to get the latest Release first (most reliable)
	release, _, relErr := client.Repositories.GetLatestRelease(ctx, owner, repo)

	if relErr == nil && release != nil {
		return release.GetTagName(), nil
	}

	// 2. If release failed, list tags and find the latest semantically
	tags, _, tagErr := client.Repositories.ListTags(ctx, owner, repo, &github.ListOptions{PerPage: 30})

	if tagErr != nil {
		return "", fmt.Errorf("could not retrieve tags: %w", tagErr)
	}

	// Iterate through tags and find the highest semantic version
	for _, tag := range tags {
		tagName := tag.GetName()
		verToCompare := tagName
		if !strings.HasPrefix(verToCompare, "v") {
			verToCompare = "v" + verToCompare
		}

		if semver.IsValid(verToCompare) {
			if latestValidVersion == "" || semver.Compare(verToCompare, latestValidVersion) > 0 {
				latestValidVersion = verToCompare
			}
		}
	}

	if latestValidVersion == "" {
		return "", fmt.Errorf("no valid semantic version tags found")
	}

	return latestValidVersion, nil
}

// checkUpdateAndCreateReport: Performs the update check
func checkUpdateAndCreateReport(client *github.Client, deps []DependencyInfo) []DependencyInfo {
	var results []DependencyInfo

	for i := range deps {
		dep := &deps[i]
		owner, repo := parseGitHubURL(dep.RepoURL)

		fmt.Printf("-> Checking %s (%s) from %s/%s\n", dep.Name, dep.CurrentVersion, owner, repo)

		currentVer := dep.CurrentVersion
		if !strings.HasPrefix(currentVer, "v") {
			currentVer = "v" + currentVer
		}

		if owner == "" || repo == "" || !semver.IsValid(currentVer) {
			dep.Status = "❌ Invalid dependency details"
			results = append(results, *dep)
			continue
		}

		latestVerWithV, err := findLatestVersion(client, owner, repo)
		if err != nil {
			dep.Status = fmt.Sprintf("❌ Error: %v", err)
			results = append(results, *dep)
			continue
		}

		if semver.Compare(latestVerWithV, currentVer) > 0 {
			dep.UpdateNeeded = true
			dep.Status = "⬆️ Update Available"
		} else {
			dep.UpdateNeeded = false
			dep.Status = "✅ Up to Date"
		}

		dep.LatestVersion = strings.TrimPrefix(latestVerWithV, "v")
		results = append(results, *dep)
	}
	return results
}

// printReport: Writes the results to the specified file in Markdown table format
func printReport(results []DependencyInfo, filename string) error {

	file, err := os.Create(filename)
	if err != nil {
		// If file creation fails, report the error up the chain
		return fmt.Errorf("could not create report file %s: %w", filename, err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	// Markdown Content
	writer.WriteString("## 📋 Erlang Dependency Update Audit\n\n")
	writer.WriteString("This report compares the current tags in your `rebar.config` against the latest versions on GitHub.\n\n")
	writer.WriteString("| # | Dependency | Status | Current Tag | Latest Tag | Repository |\n")
	writer.WriteString("| :---: | :--- | :---: | :---: | :---: | :--- |\n")

	for i, dep := range results {
		currentDisplay := strings.TrimPrefix(dep.CurrentVersion, "v")
		latestDisplay := dep.LatestVersion

		statusDisplay := dep.Status
		if dep.UpdateNeeded {
			statusDisplay = "**" + statusDisplay + "**"
		}

		owner, repo := parseGitHubURL(dep.RepoURL)

		// Link directly to the repository
		repoLink := fmt.Sprintf("[%s/%s](https://github.com/%s/%s)", owner, repo, owner, repo)
		if owner == "" {
			repoLink = dep.RepoURL // Fallback if parsing failed
		}

		line := fmt.Sprintf("| %d | `%s` | %s | `%s` | `%s` | %s |\n",
			i+1, dep.Name, statusDisplay, currentDisplay, latestDisplay, repoLink)
		writer.WriteString(line)
	}

	return nil
}

func main() {
	const configFileName = "backend/rebar.config"
	const outputDir = "backend"
	const outputFileName = "report.md"
	outputFilePath := outputDir + "/" + outputFileName

	// 1. Create the 'backend' directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		if err := os.Mkdir(outputDir, 0755); err != nil {
			fmt.Printf("Fatal Error: Could not create directory %s: %v\n", outputDir, err)
			os.Exit(1)
		}
	}

	// 2. Read the file content
	configContent, err := readConfigFile(configFileName)
	if err != nil {
		fmt.Printf("Fatal Error: %v\n", err)
		os.Exit(1)
	}

	// 3. Parse the dependencies
	deps, err := parseErlangDeps(configContent)
	if err != nil {
		fmt.Printf("Error parsing dependencies: %v\n", err)
		os.Exit(1)
	}

	// 4. Filter and prepare dependencies
	var filteredDeps []DependencyInfo
	for _, dep := range deps {
		currentVer := dep.CurrentVersion
		if !strings.HasPrefix(currentVer, "v") {
			currentVer = "v" + currentVer
		}
		// Only proceed if the current version is valid SemVer (i.e., not a branch name like "main")
		if semver.IsValid(currentVer) {
			filteredDeps = append(filteredDeps, dep)
		}
	}

	if len(filteredDeps) == 0 {
		fmt.Println("No valid Git tag dependencies found to audit.")
		return
	}

	client := createGitHubClient()

	fmt.Printf("Starting audit of %d Erlang dependencies...\n", len(filteredDeps))

	// 5. Perform the checks
	results := checkUpdateAndCreateReport(client, filteredDeps)

	// 6. Write the final Markdown report to the file
	err = printReport(results, outputFilePath)
	if err != nil {
		fmt.Printf("Fatal Error writing report: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Operation completed successfully. Results saved in **%s**.\n", outputFilePath)
}
