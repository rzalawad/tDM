package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rzalawda/tdm/go_client/internal/api"
	"github.com/rzalawda/tdm/go_client/internal/ui"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
)

func main() {
	apiClient := api.NewClient("http://localhost:54759")
	var rootCmd = &cobra.Command{
		Use:   "client",
		Short: "Download Manager Client",
		Run: func(cmd *cobra.Command, args []string) {
			runTUI(apiClient)
		},
	}

	var directoryFlag string
	var taskFlag string
	var addCmd = &cobra.Command{
		Use:   "add <url> [url2] [url3] ...",
		Short: "Add one or more downloads",
		Long:  "Add one or more URLs to the download queue. All URLs will be downloaded to the specified directory.",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			directory := directoryFlag
			if directory == "" {
				directory = "."
			}
			
			err := submitDownloads(apiClient, args, directory, taskFlag)
			if err != nil {
				log.Fatalf("Failed to submit downloads: %v", err)
			}
		},
	}
	addCmd.Flags().StringVarP(&directoryFlag, "directory", "d", "", "Directory to save downloads (default: current directory)")
	addCmd.Flags().StringVarP(&taskFlag, "task", "t", "", "Task name for grouping related downloads")

	var concurrencyCmd = &cobra.Command{
		Use:   "concurrency <value>",
		Short: "Set or view download concurrency",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {

			if len(args) == 0 {
				concurrency, err := apiClient.GetConcurrency()
				if err != nil {
					fmt.Printf("Failed to fetch concurrency: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Current concurrency: %d\n", concurrency)
			} else {
				concurrencyValue, err := strconv.Atoi(args[0])
				if err != nil {
					fmt.Printf("Invalid concurrency value: %v\n", err)
					os.Exit(1)
				}

				err = apiClient.UpdateConcurrency(concurrencyValue)
				if err != nil {
					fmt.Printf("Failed to update concurrency: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Concurrency set to: %d\n", concurrencyValue)
			}
		},
	}

	rootCmd.AddCommand(addCmd, concurrencyCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		log.Fatalf("Error executing command: %v", err)
	}
}

func submitDownloads(apiClient *api.Client, urls []string, directory string, task string) error {
	absPath, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path: %v", err)
	}

	err = apiClient.SubmitDownloads(urls, absPath, task)
	if err != nil {
		return fmt.Errorf("failed to submit downloads: %v", err)
	}
	
	if len(urls) == 1 {
		fmt.Printf("Download request for %s will be saved to %s\n", urls[0], absPath)
	} else {
		fmt.Printf("Download request for %d URLs will be saved to %s\n", len(urls), absPath)
	}
	
	return nil
}

func showDeleteDownloadConfirmation(app *tview.Application, pages *tview.Pages, table *tview.Table, row int, mainInputCapture func(event *tcell.EventKey) *tcell.EventKey) {

	apiClient := api.NewClient("http://localhost:54759")
	downloadId, err_conv := strconv.Atoi(table.GetCell(row, 0).Text)
	if err_conv != nil {
		log.Printf("Can't convert string to int: %d", table.GetCell(row, 0).Text)
		return
	}
	download, err := apiClient.FetchDownload(downloadId)
	if err != nil {
		log.Printf("Can't get download for downloadId: %d", downloadId)
		return
	}

	form := tview.NewForm().AddTextView(
		"",
		fmt.Sprintf(
			"[red]Confirm Download Delete? (Y/N)[-]\n\n"+
				"[::b]URL:[-]         %s\n"+
				"[::b]Gid:[-]         %s\n"+
				"[::b]Status:[-]      %s\n"+
				"[::b]Directory:[-]   %s\n"+
				"[::b]Speed:[-]       %s\n"+
				"[::b]Downloaded:[-]  %s\n"+
				"[::b]Total Size:[-]  %s\n"+
				"[::b]Date Added:[-]  %s\n"+
				"[::b]Progress:[-]    %s\n"+
				"[::b]Error:[-]       %s",
			table.GetCell(row, 1).Text,
			download.Gid,
			table.GetCell(row, 2).Text,
			table.GetCell(row, 3).Text,
			table.GetCell(row, 4).Text,
			table.GetCell(row, 5).Text,
			table.GetCell(row, 6).Text,
			table.GetCell(row, 7).Text,
			table.GetCell(row, 8).Text,
			download.Error,
		), 0, 400, true, true)
	form.SetBorder(true).SetTitle("Delete Download?")

	pages.AddPage("delete", form, true, true)
	pages.SwitchToPage("delete")

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape ||
			event.Rune() == 'n' ||
			event.Rune() == 'N' ||
			event.Rune() == 'q' ||
			event.Rune() == 'Q' {
			pages.RemovePage("delete")
			pages.SwitchToPage("main")
			app.SetInputCapture(mainInputCapture)
			return nil
		}
		if event.Rune() == 'y' || event.Rune() == 'Y' {
			downloadId, err_conv := strconv.Atoi(table.GetCell(row, 0).Text)
			if err_conv == nil {
				apiClient.DeleteDownload(downloadId)
			} else {
				log.Printf("Can't convert string to int: %d", table.GetCell(row, 0).Text)
			}
			pages.RemovePage("delete")
			pages.SwitchToPage("main")
			app.SetInputCapture(mainInputCapture)
			return nil
		}
		return event
	})
}

func runTUI(apiClient *api.Client) {
	logDir := "/tmp/download-manager-client"
	err := os.MkdirAll(logDir, 0755)
	if err != nil {
		fmt.Printf("Failed to create log directory: %v\n", err)
		os.Exit(1)
	}

	logPath := filepath.Join(logDir, fmt.Sprintf("app_%d.log", time.Now().Unix()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("Failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Starting Download Manager Client TUI")

	app := tview.NewApplication()

	concurrency, err := apiClient.GetConcurrency()
	if err != nil {
		log.Printf("Failed to fetch concurrency: %v", err)
		concurrency = 1
	}
	log.Printf("Initial concurrency set to: %d", concurrency)
	maxDownloadSpeed := 0

	pages := tview.NewPages()
	table := ui.NewDownloadsTable(nil, app, pages)
	layout, settingsView, _ := ui.CreateLayoutWithTable(table, nil, concurrency, maxDownloadSpeed)
	pages.AddPage("main", layout, true, true)

	refreshDownloads := func() {
		log.Println("Fetching downloads...")
		downloads, err := apiClient.FetchDownloads()
		if err != nil {
			log.Printf("Failed to fetch downloads: %v", err)
			app.QueueUpdateDraw(func() {
				modal := tview.NewModal().
					SetText("Connection to server failed.").
					AddButtons([]string{"Exit"}).
					SetDoneFunc(func(buttonIndex int, buttonLabel string) {
						app.Stop()
					})
				app.SetRoot(modal, false)
			})
			return
		}
		log.Println("Downloads fetched successfully.")
		app.QueueUpdateDraw(func() {
			log.Printf("Updating table with new downloads... (current row count: %d)", table.GetRowCount())
			ui.UpdateDownloadsTable(table, downloads)
			ui.UpdateSettingsView(settingsView, downloads, concurrency, maxDownloadSpeed)
			r, c := table.GetOffset()
			log.Printf("Table updated. Row count: %d, Current offset: row=%d, col=%d",
				table.GetRowCount(), r, c)
		})
	}

	// Create a channel to signal goroutine shutdown
	done := make(chan struct{})
	defer close(done)

	// Declare mainInputCapture variable first
	var mainInputCapture func(event *tcell.EventKey) *tcell.EventKey

	// Define the function after declaration
	mainInputCapture = func(event *tcell.EventKey) *tcell.EventKey {
		log.Printf("Key event received: %v, Rune: %c", event.Key(), event.Rune())

		if event.Key() == tcell.KeyCtrlC || event.Rune() == 'q' || event.Rune() == 'Q' {
			log.Println("Exiting application...")
			app.Stop()
			return nil
		}

		if event.Rune() == 'i' || event.Rune() == 'I' {
			row, _ := table.GetSelection()
			log.Printf("Enter pressed on row: %d", row)
			if row > 0 { // Ignore header row
				log.Printf("Showing detailed view for row: %d", row)
				ui.ShowDetailedView(app, pages, table, row, mainInputCapture)
				return nil
			}
		}

		if event.Rune() == 'd' || event.Rune() == 'D' {
			row, _ := table.GetSelection()
			log.Printf("Enter pressed on row: %d", row)
			if row > 0 { // Ignore header row
				log.Printf("Prompting to delete download: %d", row)
				showDeleteDownloadConfirmation(app, pages, table, row, mainInputCapture)
				return nil
			}
		}

		if event.Rune() == 'c' || event.Rune() == 'C' {
			log.Println("Concurrency setting mode activated")
			var inputField *tview.InputField
			inputField = tview.NewInputField().
				SetLabel("Set Concurrency: ").
				SetFieldWidth(10).
				SetAcceptanceFunc(tview.InputFieldInteger).
				SetDoneFunc(func(key tcell.Key) {
					log.Printf("Input field done func called with key: %v", key)
					if key == tcell.KeyEnter {
						newConcurrency := inputField.GetText()
						log.Printf("New concurrency input: %s", newConcurrency)

						if newConcurrency != "" {
							concurrencyValue, err := strconv.Atoi(newConcurrency)
							if err != nil {
								log.Printf("Invalid concurrency value: %v", err)
								return
							}
							log.Printf("Attempting to update concurrency to %d", concurrencyValue)
							err = apiClient.UpdateConcurrency(concurrencyValue)
							if err != nil {
								log.Printf("Failed to update concurrency: %v", err)
							} else {
								log.Printf("Concurrency updated successfully to %d", concurrencyValue)
								concurrency = concurrencyValue
							}
						}
					}
					log.Printf("Launching Switch Go Routine")
					pages.RemovePage("concurrency")
					pages.SwitchToPage("main")
					app.SetFocus(table)
					log.Printf("Appview switched to main page")
				})

			pages.AddPage("concurrency", inputField, true, true)
			pages.SwitchToPage("concurrency")
			app.SetFocus(inputField)
			return nil
		}
		return event
	}

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				log.Println("Stopping refresh goroutine...")
				return
			case <-ticker.C:
				if pages.HasPage("concurrency") {
					continue
				}
				log.Println("Periodic refresh triggered")
				refreshDownloads()
			}
		}
	}()

	log.Println("Setting initial root and running application")

	go func() {
		log.Println("Waiting for app to be ready...")
		log.Println("App is ready, proceeding with setup")
		log.Printf("Starting initial refresh, current row count: %d", table.GetRowCount())
		refreshDownloads()

		app.QueueUpdateDraw(func() {
			rowCount := table.GetRowCount()
			log.Printf("Preparing to set scroll position, row count: %d", rowCount)

			if rowCount > 1 {
				r, c := table.GetOffset()
				log.Printf("Current offset before scroll: row=%d, col=%d", r, c)
				log.Println("Setting initial scroll position after first refresh")
				table.Select(1, 0)
				r, c = table.GetOffset()
				log.Printf("New offset after scroll: row=%d, col=%d", r, c)
				log.Printf("Initial scroll position set, final row count: %d", table.GetRowCount())
			} else {
				log.Printf("Not setting scroll position, insufficient rows: %d", rowCount)
			}
		})
	}()

	if err := app.SetInputCapture(mainInputCapture).SetRoot(pages, true).SetFocus(pages).Run(); err != nil {
		log.Fatalf("Error running application: %v", err)
	}

	log.Println("Application exited cleanly")
}
