package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateFilePath ensures that the file path is safe and prevents directory traversal attacks
func validateFilePath(baseDir, filePath string) (string, error) {
	// Clean the file path to remove any .. or . components
	cleanPath := filepath.Clean(filePath)
	
	// Check for path traversal attempts
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("path traversal detected: %s", filePath)
	}
	
	// Join with base directory and clean again
	fullPath := filepath.Join(baseDir, cleanPath)
	fullPath = filepath.Clean(fullPath)
	
	// Ensure the resulting path is still within the base directory
	relPath, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to get relative path: %w", err)
	}
	
	if strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("path traversal detected: %s", filePath)
	}
	
	return fullPath, nil
}
