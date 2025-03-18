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

	"github.com/rzalawad/tdm/server_go/pkg/core"
)

// WorkProcessor handles post-download tasks like unpacking and moving files
type WorkProcessor struct {
	tempDir string
	running bool
	wg      sync.WaitGroup
}

// NewWorkProcessor creates a new work processor
func NewWorkProcessor(tempDir string) *WorkProcessor {
	return &WorkProcessor{
		tempDir: tempDir,
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

				// Get all tasks for this group
				var tasks []core.Task
				if err := db.Where("group_id = ?", group.ID).Order("id").Find(&tasks).Error; err != nil {
					log.Printf("Error querying tasks for group %d: %v", group.ID, err)
					continue
				}

				log.Printf("Group %d has %d tasks", group.ID, len(tasks))
				for i, t := range tasks {
					log.Printf("  Task %d: ID=%d, Type=%s, Status=%s", i+1, t.ID, t.TaskType, t.Status)
				}

				// Find pending task
				var pendingTask *core.Task
				for i, task := range tasks {
					if task.Status == core.StatusPending {
						pendingTask = &tasks[i]
						break
					}
				}

				if pendingTask == nil {
					// Should not happen as we queried for pending tasks
					continue
				}

				// Get required download status for this task type
				targetDownloadStatus, ok := core.TaskTypeToStatus[pendingTask.TaskType]
				if !ok {
					log.Printf("Unknown task type: %s", pendingTask.TaskType)
					pendingTask.Status = core.StatusFailed
					errMsg := fmt.Sprintf("Unknown task type: %s", pendingTask.TaskType)
					pendingTask.Error = &errMsg
					db.Save(&pendingTask)
					continue
				}

				// Check if all downloads are in the right status
				allDownloadsReady := true
				for _, download := range downloads {
					if download.Status != targetDownloadStatus {
						allDownloadsReady = false
						break
					}
				}

				if !allDownloadsReady {
					// Skip this task for now
					continue
				}

				// Process the task
				log.Printf("Processing task %d of type %s for group %d", pendingTask.ID, pendingTask.TaskType, group.ID)

				switch pendingTask.TaskType {
				case core.TaskTypeMove:
					w.performMoveTask(pendingTask, downloads)
				case core.TaskTypeUnpack:
					w.performUnpackTask(pendingTask, downloads)
				default:
					errMsg := fmt.Sprintf("Unknown task type: %s", pendingTask.TaskType)
					pendingTask.Status = core.StatusFailed
					pendingTask.Error = &errMsg
					db.Save(pendingTask)
					continue
				}

				// If task completed successfully, update download statuses to next task
				if pendingTask.Status == core.StatusCompleted {
					// Find next task if any
					var nextTask *core.Task
					for i, task := range tasks {
						if task.ID == pendingTask.ID && i < len(tasks)-1 {
							nextTask = &tasks[i+1]
							break
						}
					}

					// Update download status
					for i := range downloads {
						if nextTask != nil {
							nextStatus, ok := core.TaskTypeToStatus[nextTask.TaskType]
							if ok {
								downloads[i].Status = nextStatus
							} else {
								downloads[i].Status = core.StatusCompleted
							}
						} else {
							downloads[i].Status = core.StatusCompleted
						}
						db.Save(&downloads[i])
					}
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
	source := filepath.Join(w.tempDir, fmt.Sprintf("%d", task.GroupID))
	if w.tempDir == "" {
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

	// Copy each entry to destination
	for _, entry := range entries {
		srcPath := filepath.Join(source, entry.Name())
		dstPath := filepath.Join(destination, entry.Name())

		if entry.IsDir() {
			// For directories, copy the entire directory recursively
			err = filepath.WalkDir(srcPath, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				// Get path relative to the subdirectory
				relPath, err := filepath.Rel(srcPath, path)
				if err != nil {
					return err
				}

				// Skip the root of the subdirectory
				if relPath == "." {
					return nil
				}

				fullDestPath := filepath.Join(dstPath, relPath)
				log.Printf("Copying %s to %s", path, fullDestPath)

				if d.IsDir() {
					log.Printf("Creating directory %s", fullDestPath)
					return os.MkdirAll(fullDestPath, 0755)
				}

				return copyFile(path, fullDestPath)
			})
		} else {
			// For files, just copy the file
			err = copyFile(srcPath, dstPath)
		}

		if err != nil {
			log.Printf("Error copying %s: %v", entry.Name(), err)
			task.Status = core.StatusFailed
			errMsg := fmt.Sprintf("Error copying files: %v", err)
			task.Error = &errMsg

			group.Status = core.GroupStatusFailed
			group.Error = &errMsg

			db.Save(task)
			db.Save(&group)
			return
		}
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
	source := filepath.Join(w.tempDir, fmt.Sprintf("%d", task.GroupID))
	if w.tempDir == "" {
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
