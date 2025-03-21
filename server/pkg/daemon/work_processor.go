package daemon

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rzalawad/tdm/server/pkg/core"
)

// WorkProcessor handles post-download tasks like unpacking and moving files
type WorkProcessor struct {
	running bool
	wg      sync.WaitGroup
	config  *core.DaemonConfig
}

// NewWorkProcessor creates a new work processor
func NewWorkProcessor(config *core.DaemonConfig) *WorkProcessor {
	return &WorkProcessor{
		config:  config,
		running: false,
	}
}

// Start launches the work processor
func (w *WorkProcessor) Start() {
	if w.running {
		return
	}

	w.running = true
	w.wg.Add(1)

	go func() {
		defer w.wg.Done()
		log.Println("Starting task processor thread")

		for w.running {
			db := core.GetDB()

			// Find groups with pending tasks
			var groups []core.Group
			err := db.Joins("JOIN tasks ON tasks.group_id = groups.id").
				Where("tasks.status = ?", core.StatusPending).
				Distinct().
				Find(&groups).Error

			if err != nil {
				log.Printf("Error querying groups with pending tasks: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}

			for _, group := range groups {
				// Get all downloads for this group
				var downloads []core.Download
				if err := db.Where("group_id = ?", group.ID).Find(&downloads).Error; err != nil {
					log.Printf("Error querying downloads for group %d: %v", group.ID, err)
					continue
				}

				// Check if all downloads are in the right status
				allDownloadsReady := true
				for _, download := range downloads {
					if download.Status != core.StatusDownloaded {
						allDownloadsReady = false
						break
					}
				}

				if !allDownloadsReady {
					// Skip this task for now
					continue
				}

				// Get all tasks for this group
				var tasks []core.Task
				if err := db.Where("group_id = ?", group.ID).Order("id").Find(&tasks).Error; err != nil {
					log.Printf("Error querying tasks for group %d: %v", group.ID, err)
					continue
				}

				// Process tasks in order
				for _, task := range tasks {
					// Process the task
					log.Printf("Processing task %d of type %s for group %d", task.ID, task.TaskType, group.ID)

					// Update download status for all downloads in this group
					isValidTask := true
					for i := range downloads {
						status, exists := core.TaskTypeToStatus[task.TaskType]
						if exists {
							downloads[i].Status = status
						} else {
							downloads[i].Status = core.StatusFailed
							errMsg := fmt.Sprintf("Unknown task type: %s", task.TaskType)
							downloads[i].Error = &errMsg
							db.Save(&downloads[i])
							log.Printf("Unknown task type: %s", task.TaskType)
							isValidTask = false
						}
						// Apply the status and save
						db.Save(&downloads[i])
					}
					if !isValidTask {
						continue
					}

					switch task.TaskType {
					case core.TaskTypeMove:
						w.performMoveTask(&task, downloads)
					case core.TaskTypeUnpack:
						w.performUnpackTask(&task, downloads)
					case core.TaskTypeOrganize:
						w.performOrganizeTask(&task, downloads)
					default:
						errMsg := fmt.Sprintf("Unknown task type: %s", task.TaskType)
						task.Status = core.StatusFailed
						task.Error = &errMsg
						db.Save(task)
						continue
					}
				}
				for i := range downloads {
					downloads[i].Status = core.StatusCompleted
					downloads[i].Error = nil
					db.Save(&downloads[i])
				}

			}

			// Sleep before next check
			time.Sleep(1 * time.Second)
		}

		log.Println("Task processor thread stopped")
	}()
}

// Wait waits for the work processor to finish
func (w *WorkProcessor) Wait() {
	w.wg.Wait()
}

// Stop shuts down the work processor
func (w *WorkProcessor) Stop() {
	w.running = false
}

// performOrganizeTask organizers files based on the provided organize script to create folders
func (w *WorkProcessor) performOrganizeTask(task *core.Task, downloads []core.Download) {
	log.Printf("Performing organize task for group %d", task.GroupID)
	db := core.GetDB()

	downloadDirectory := filepath.Join(w.config.TemporaryDownloadDirectory, fmt.Sprintf("%d", task.GroupID))
	if w.config.TemporaryDownloadDirectory == "" {
		downloadDirectory = downloads[0].Directory
	}

	script := w.config.Organize
	if script == "" {
		log.Printf("No organize script provided, skipping organize task for group %d", task.GroupID)
		return
	}

	cmd := exec.Command("sh", "-c", fmt.Sprintf("%s %s", script, downloadDirectory))
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error running organize script: %v", err)
		log.Printf("Output: %s", string(output))
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error running organize script: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	log.Printf("Successfully ran organize script: %s", output)
	task.Status = core.StatusCompleted
	task.Error = nil
	db.Save(task)

}

// performMoveTask moves files from the temporary directory to the final directory
func (w *WorkProcessor) performMoveTask(task *core.Task, downloads []core.Download) {
	log.Printf("Performing move task for group %d", task.GroupID)
	db := core.GetDB()

	// Get the group
	var group core.Group
	if err := db.First(&group, task.GroupID).Error; err != nil {
		log.Printf("Error retrieving group %d: %v", task.GroupID, err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error retrieving group: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Check if we have downloads
	if len(downloads) == 0 {
		log.Printf("No downloads found for group ID: %d", task.GroupID)
		task.Status = core.StatusFailed
		errMsg := "No downloads found for group"
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Source is the temporary download path or the first download's directory
	// with the group ID as a subdirectory
	log.Printf("Temporary download directory: %s", w.config.TemporaryDownloadDirectory)
	source := filepath.Join(w.config.TemporaryDownloadDirectory, fmt.Sprintf("%d", task.GroupID))
	if w.config.TemporaryDownloadDirectory == "" {
		source = filepath.Join(downloads[0].Directory, fmt.Sprintf("%d", task.GroupID))
	}

	// Destination is the directory from the first download
	destination := downloads[0].Directory

	// Check if source exists
	if _, err := os.Stat(source); os.IsNotExist(err) {
		log.Printf("Source file not found: %s", source)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Source file not found: %s", source)
		task.Error = &errMsg

		group.Status = core.GroupStatusFailed
		group.Error = &errMsg

		db.Save(&task)
		db.Save(&group)
		return
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destination, 0755); err != nil {
		log.Printf("Error creating destination directory: %v", err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error creating destination directory: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Read the source directory entries and copy them to destination
	entries, err := os.ReadDir(source)
	if err != nil {
		log.Printf("Error reading source directory: %v", err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error reading source directory: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}
	for _, entry := range entries {
		log.Printf("Entry: %s %t", entry.Name(), entry.IsDir())
	}

	// Walk the entire source directory and copy to destination
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get path relative to the source directory
		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		// Skip the root of the source directory
		if relPath == "." {
			return nil
		}

		fullDestPath := filepath.Join(destination, relPath)

		if d.IsDir() {
			log.Printf("Creating directory: %s", fullDestPath)
			return os.MkdirAll(fullDestPath, 0755)
		}

		log.Printf("Copying file: %s to %s", path, fullDestPath)
		return copyFile(path, fullDestPath)
	})

	if err != nil {
		log.Printf("Error copying files: %v", err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error copying files: %v", err)
		task.Error = &errMsg

		group.Status = core.GroupStatusFailed
		group.Error = &errMsg

		db.Save(task)
		db.Save(&group)
		return
	}

	// Remove source directory after successful copy
	if err := os.RemoveAll(source); err != nil {
		log.Printf("Warning: Error removing source directory: %v", err)
	} else {
		log.Printf("Removed source directory: %s", source)
	}

	// Mark task as completed
	task.Status = core.StatusCompleted
	task.Error = nil

	// Mark group as completed if it's the last task
	var pendingTasks int64
	db.Model(&core.Task{}).Where("group_id = ? AND status = ?", group.ID, core.StatusPending).Count(&pendingTasks)

	if pendingTasks == 0 {
		group.Status = core.GroupStatusCompleted
		group.Error = nil
	}

	db.Save(&task)
	db.Save(&group)

	log.Printf("Successfully moved files from %s to %s", source, destination)
}

// performUnpackTask unpacks archives in the download directory
func (w *WorkProcessor) performUnpackTask(task *core.Task, downloads []core.Download) {
	db := core.GetDB()

	// Get the group
	var group core.Group
	if err := db.First(&group, task.GroupID).Error; err != nil {
		log.Printf("Error retrieving group %d: %v", task.GroupID, err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error retrieving group: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Check if we have downloads
	if len(downloads) == 0 {
		log.Printf("No downloads found for group ID: %d", task.GroupID)
		task.Status = core.StatusFailed
		errMsg := "No downloads found for group"
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Source directory (where we'll look for archives)
	source := filepath.Join(w.config.TemporaryDownloadDirectory, fmt.Sprintf("%d", task.GroupID))

	if w.config.TemporaryDownloadDirectory == "" {
		source = filepath.Join(downloads[0].Directory, fmt.Sprintf("%d", task.GroupID))
	}

	// Check if source exists
	if _, err := os.Stat(source); os.IsNotExist(err) {
		log.Printf("Source directory not found: %s", source)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Source directory not found: %s", source)
		task.Error = &errMsg

		group.Status = core.GroupStatusFailed
		group.Error = &errMsg

		db.Save(&task)
		db.Save(&group)
		return
	}

	// Get a list of files in the source directory
	files, err := os.ReadDir(source)
	if err != nil {
		log.Printf("Error reading source directory: %v", err)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("Error reading source directory: %v", err)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	if len(files) == 0 {
		log.Printf("No files found in source directory: %s", source)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("No files found in source directory: %s", source)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Extract the first archive
	extractedFile := false
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		filePath := filepath.Join(source, fileName)

		// Check if it's a supported archive type
		ext := filepath.Ext(fileName)
		if ext == ".zip" || ext == ".tar" || ext == ".gz" || ext == ".bz2" {
			// Extract with Go's built-in archive function
			if err := extractArchive(filePath, source); err != nil {
				log.Printf("Failed to extract archive: %v", err)
				continue
			}
			// Delete all original files after extraction
			for _, file := range files {
				if err := os.Remove(filepath.Join(source, file.Name())); err != nil {
					log.Printf("Warning: Failed to delete file after extraction: %v", err)
				}
			}
			extractedFile = true
			break
		} else if ext == ".rar" {
			// Check if unrar is installed
			if _, err := exec.LookPath("unrar"); err != nil {
				log.Printf("unrar is not installed")
				task.Status = core.StatusFailed
				errMsg := "unrar is not installed"
				task.Error = &errMsg
				db.Save(task)
				return
			}

			// Extract with unrar command
			cmd := exec.Command("unrar", "x", filePath, source)
			if err := cmd.Run(); err != nil {
				log.Printf("Failed to extract RAR archive: %v", err)
				task.Status = core.StatusFailed
				errMsg := fmt.Sprintf("Failed to extract RAR archive: %v", err)
				task.Error = &errMsg
				db.Save(task)
				return
			}

			// Delete all original files after extraction
			for _, file := range files {
				if err := os.Remove(filepath.Join(source, file.Name())); err != nil {
					log.Printf("Warning: Failed to delete file after extraction: %v", err)
				}
			}
			extractedFile = true
			break
		}
	}

	if !extractedFile {
		log.Printf("No supported archive found in source directory: %s", source)
		task.Status = core.StatusFailed
		errMsg := fmt.Sprintf("No supported archive found in source directory: %s", source)
		task.Error = &errMsg
		db.Save(task)
		return
	}

	// Mark task as completed
	task.Status = core.StatusCompleted
	task.Error = nil
	db.Save(task)

	log.Printf("Successfully unpacked files in %s", source)
}

// extractArchive extracts a ZIP, TAR, or other archive to the given destination
func extractArchive(archivePath, destPath string) error {
	// For now, just use OS command for simplicity
	// For a real implementation, you'd want to use Go's archive packages
	cmd := exec.Command("tar", "-xf", archivePath, "-C", destPath)
	return cmd.Run()
}

// copyFile copies a single file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
