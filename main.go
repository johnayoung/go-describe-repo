package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	gitignore "github.com/sabhiram/go-gitignore"
	"github.com/sashabaranov/go-openai"
)

type ProjectContext struct {
	Context     Context           `json:"context"`
	CurrentCode map[string]string `json:"current_code"`
}

type Context struct {
	ProjectName        string   `json:"project_name"`
	ProjectDescription string   `json:"project_description"`
	FileStructure      []string `json:"file_structure"`
}

func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file")
	}
}

func readGitignore(path string) (*gitignore.GitIgnore, error) {
	file, err := os.Open(filepath.Join(path, ".gitignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		patterns = append(patterns, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return gitignore.CompileIgnoreLines(patterns...), nil
}

func getRepoDetails(path string) (string, []string, string, map[string]string, error) {
	gitignore, err := readGitignore(path)
	if err != nil {
		return "", nil, "", nil, err
	}

	var fileStructure []string
	currentCode := make(map[string]string)
	err = filepath.Walk(path, func(filePath string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(path, filePath)
		if err != nil {
			return err
		}

		if gitignore != nil && gitignore.MatchesPath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		if !info.IsDir() {
			fileStructure = append(fileStructure, relPath)
			content, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			currentCode[relPath] = string(content)
		}
		return nil
	})
	if err != nil {
		return "", nil, "", nil, err
	}

	langs := make(map[string]int)
	filepath.Walk(path, func(filePath string, info fs.FileInfo, err error) error {
		if !info.IsDir() {
			ext := filepath.Ext(info.Name())
			langs[ext]++
		}
		return nil
	})

	var primaryLang string
	maxCount := 0
	for lang, count := range langs {
		if count > maxCount {
			primaryLang = lang
			maxCount = count
		}
	}

	entryPoint := "main." + strings.TrimPrefix(primaryLang, ".")

	return primaryLang, fileStructure, entryPoint, currentCode, nil
}

func generatePrompt(primaryLang string, fileStructure []string, entryPoint string) string {
	fileStructureStr := strings.Join(fileStructure, "\n")
	return fmt.Sprintf(
		"Primary Language: %s\n\n"+
			"File Structure:\n%s\n\n"+
			"Entry Point: %s\n\n"+
			"Based on the above information, please:\n"+
			"1. Describe the purpose of the project.\n"+
			"2. Provide a best guess description of the components and how they work with one another.\n",
		primaryLang, fileStructureStr, entryPoint,
	)
}

func callOpenAI(prompt string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	client := openai.NewClient(apiKey)
	resp, err := client.CreateChatCompletion(context.TODO(), openai.ChatCompletionRequest{
		Model: openai.GPT4o20240513,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "system",
				Content: "You are a helpful assistant.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}

func safeFileName(path string) string {
	return strings.ReplaceAll(strings.ReplaceAll(path, "/", "_"), "\\", "_")
}

func main() {
	loadEnv()

	if len(os.Args) < 2 {
		log.Fatal("Please provide a directory path")
	}
	dirPath := os.Args[1]

	projectName := filepath.Base(dirPath)
	outputDir := filepath.Join("data", safeFileName(dirPath))
	err := os.MkdirAll(outputDir, 0755)
	if err != nil {
		log.Fatalf("Failed to create directories: %v", err)
	}

	primaryLang, fileStructure, entryPoint, currentCode, err := getRepoDetails(dirPath)
	if err != nil {
		log.Fatalf("Failed to get repo details: %v", err)
	}

	initialPrompt := generatePrompt(primaryLang, fileStructure, entryPoint)
	fmt.Println("Initial Prompt:")
	fmt.Println(initialPrompt)

	finalPrompt, err := callOpenAI(initialPrompt)
	if err != nil {
		log.Fatalf("Failed to call OpenAI: %v", err)
	}

	projectContext := ProjectContext{
		Context: Context{
			ProjectName:        projectName,
			ProjectDescription: finalPrompt,
			FileStructure:      fileStructure,
		},
		CurrentCode: currentCode,
	}

	jsonData, err := json.MarshalIndent(projectContext, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal JSON: %v", err)
	}

	jsonFilePath := filepath.Join(outputDir, "project_context.json")
	err = os.WriteFile(jsonFilePath, jsonData, 0644)
	if err != nil {
		log.Fatalf("Failed to write JSON file: %v", err)
	}

	fmt.Printf("Project context written to %s\n", jsonFilePath)

	// Read the JSON file contents to create a new prompt
	jsonContent, err := os.ReadFile(jsonFilePath)
	if err != nil {
		log.Fatalf("Failed to read JSON file: %v", err)
	}

	newPrompt := fmt.Sprintf(
		"Take in the following json data, and attempt to write a detailed project description based off of the components and their interactions with one another:\n\n%s",
		string(jsonContent),
	)

	projectDescription, err := callOpenAI(newPrompt)
	if err != nil {
		log.Fatalf("Failed to call OpenAI for project description: %v", err)
	}

	mdFilePath := filepath.Join(outputDir, "project_description.md")
	err = os.WriteFile(mdFilePath, []byte(projectDescription), 0644)
	if err != nil {
		log.Fatalf("Failed to write Markdown file: %v", err)
	}

	fmt.Printf("Project description written to %s\n", mdFilePath)
}
