package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct manages desktop lifecycle and native OS dialog bindings.
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// domReady is called after the front-end has finished loading.
func (a *App) domReady(ctx context.Context) {
	// Add runtime initialization logic if needed
}

// shutdown is called at application termination.
func (a *App) shutdown(ctx context.Context) {
	// Clean up resources
}

// SelectDirectory opens a native Linux GTK/KDE folder picker dialog.
func (a *App) SelectDirectory(defaultPath string) (string, error) {
	if defaultPath == "" {
		defaultPath = "/home"
	}

	selectedDir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		DefaultDirectory:           defaultPath,
		Title:                      "Select Directory to Scan & Index",
		CanCreateDirectories:       false,
		TreatPackagesAsDirectories: false,
	})

	if err != nil {
		return "", fmt.Errorf("failed to open directory picker: %w", err)
	}

	if selectedDir != "" {
		return filepath.Clean(selectedDir), nil
	}

	return "", nil
}

// ShowNotification displays a native OS desktop notification.
func (a *App) ShowNotification(title string, message string) {
	runtime.EventsEmit(a.ctx, "notify", map[string]string{
		"title":   title,
		"message": message,
	})
}
