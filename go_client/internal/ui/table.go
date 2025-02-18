package ui

import (
	"download-manager-client/internal/api"
	"fmt"
	"log"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func NewDownloadsTable(downloads []api.Download) *tview.Table {
	table := tview.NewTable().
		SetBorders(false).
		SetFixed(1, 0).
		SetSelectable(true, false).
		SetEvaluateAllRows(false).
		SetWrapSelection(true, true).
		SetSelectedStyle(tcell.StyleDefault.
			Foreground(tcell.ColorLightGreen).
			Background(tcell.ColorBlack).Bold(true),
		)
	table.SetSelectionChangedFunc(func(row, column int) {
		if row == 0 {
			return
		}

		cols := table.GetColumnCount()
		for r := 1; r < table.GetRowCount(); r++ {
			for c := 0; c < cols; c++ {
				cell := table.GetCell(r, c)
				if c == 2 {
					status := cell.Text
					var color tcell.Color
					switch status {
					case "in_progress":
						color = tcell.ColorGreen
					case "pending":
						color = tcell.ColorYellow
					case "failed":
						color = tcell.ColorRed
					case "completed":
						color = tcell.ColorBlue
					default:
						color = tcell.ColorWhite
					}
					cell.SetStyle(tcell.StyleDefault.
						Foreground(color).
						Background(tcell.ColorDefault))
				} else {
					cell.SetStyle(tcell.StyleDefault.
						Foreground(tcell.ColorLightGray).
						Background(tcell.ColorDefault))
				}

			}
		}
	})

	headers := []string{"ID", "URL", "Status", "Directory", "Speed", "Downloaded", "Total Size", "Date Added", "Progress"}
	for i, header := range headers {
		cell := tview.NewTableCell(header).
			SetTextColor(tcell.ColorLightGray).
			SetSelectable(false)

		if i == 0 {
			cell.SetAlign(tview.AlignRight)
		} else if i == 1 {
			cell.SetExpansion(2)
			cell.SetAlign(tview.AlignLeft)
		} else if i == 3 {
			cell.SetExpansion(1)
			cell.SetAlign(tview.AlignLeft)
		} else {
			cell.SetAlign(tview.AlignLeft)
		}

		table.SetCell(0, i, cell)
	}

	if downloads != nil {
		UpdateDownloadsTable(table, downloads)
	}

	table.ScrollToBeginning()

	return table
}

func UpdateDownloadsTable(table *tview.Table, downloads []api.Download) {
	start := time.Now()
	log.Printf("Updating table with %d downloads...", len(downloads))

	for i := table.GetRowCount() - 1; i > 0; i-- {
		table.RemoveRow(i)
	}

	for i, download := range downloads {
		var statusColor tcell.Color
		switch download.Status {
		case "in_progress":
			statusColor = tcell.ColorGreen
		case "pending":
			statusColor = tcell.ColorYellow
		case "failed":
			statusColor = tcell.ColorRed
		case "completed":
			statusColor = tcell.ColorBlue
		default:
			statusColor = tcell.ColorWhite
		}

		createCell := func(text string, textColor tcell.Color, align int) *tview.TableCell {
			return tview.NewTableCell(text).
				SetTextColor(textColor).
				SetAlign(align)
		}

		table.SetCell(i+1, 0, createCell(fmt.Sprintf("%d", download.ID),
			tcell.ColorLightGray, tview.AlignRight))

		urlCell := createCell(download.URL, tcell.ColorLightGray, tview.AlignLeft)
		urlCell.SetExpansion(2)
		table.SetCell(i+1, 1, urlCell)

		table.SetCell(i+1, 2, createCell(download.Status, statusColor, tview.AlignLeft))

		dirCell := createCell(download.Directory, tcell.ColorLightGray, tview.AlignLeft)
		dirCell.SetExpansion(1)
		table.SetCell(i+1, 3, dirCell)

		var speedText string
		if download.Speed == "N/A" {
			speedText = download.Speed
		} else {
			var speedValue float64
			fmt.Sscanf(download.Speed, "%f", &speedValue)

			if speedValue >= 1024 {
				speedText = fmt.Sprintf("%.1f MB/s", speedValue/1024)
			} else {
				speedText = fmt.Sprintf("%d KB/s", int(speedValue))
			}
		}
		table.SetCell(i+1, 4, createCell(speedText,
			tcell.ColorLightGray, tview.AlignLeft))
		formatSize := func(bytes int) string {
			if bytes == 0 {
				return "0 KB"
			}

			kb := float64(bytes) / 1024
			if kb < 1024 {
				if kb < 1 {
					return "1 KB"
				}
				return fmt.Sprintf("%d KB", int(kb))
			}

			mb := kb / 1024
			if mb < 1024 {
				return fmt.Sprintf("%.1f MB", mb)
			}

			gb := mb / 1024
			return fmt.Sprintf("%.2f GB", gb)
		}

		table.SetCell(i+1, 5, createCell(formatSize(download.Downloaded),
			tcell.ColorLightGray, tview.AlignLeft))
		table.SetCell(i+1, 6, createCell(formatSize(download.TotalSize),
			tcell.ColorLightGray, tview.AlignLeft))
		table.SetCell(i+1, 7, createCell(download.DateAdded,
			tcell.ColorLightGray, tview.AlignLeft))
		table.SetCell(i+1, 8, createCell(download.Progress,
			tcell.ColorLightGray, tview.AlignLeft))
	}
	duration := time.Since(start)
	log.Printf("Table update complete. Took %v", duration)
}

func CreateLayoutWithTable(table *tview.Table, downloads []api.Download, concurrency int, maxDownloadSpeed int) (*tview.Flex, *tview.TextView, *tview.TextView) {
	settingsText := fmt.Sprintf("[::b]SETTINGS[::-]\n"+
		"Concurrency:     %d\n"+
		"Max Speed:       %d KB/s\n"+
		"Total Downloads: %d\n"+
		"Active:          %d\n"+
		"Completed:       %d",
		concurrency,
		maxDownloadSpeed,
		len(downloads),
		countActiveDownloads(downloads),
		countCompletedDownloads(downloads))
	settingsView := tview.NewTextView().
		SetText(settingsText).
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetTextColor(tcell.ColorLightGray)

	keymapText := "[::b]KEYMAPS[::-]\n" +
		"c: Set Concurrency\n" +
		"s: Set Max Speed\n" +
		"p: Pause/Resume\n" +
		"d: Delete Download\n" +
		"r: Retry Failed\n" +
		"q: Quit"
	keymapView := tview.NewTextView().
		SetText(keymapText).
		SetTextAlign(tview.AlignRight).
		SetDynamicColors(true).
		SetTextColor(tcell.ColorLightGray)

	if downloads != nil {
		UpdateDownloadsTable(table, downloads)
	}

	header := tview.NewFlex().
		AddItem(settingsView, 0, 1, false).
		AddItem(keymapView, 0, 1, false)

	tableWrapper := tview.NewPages().
		AddPage("main", table, true, true)
	tableWrapper.SetBorder(true).
		SetTitle("Downloads").
		SetTitleColor(tcell.ColorYellow).
		SetBorderColor(tcell.ColorDarkGray)

	if table.GetRowCount() > 1 {
		table.Select(1, 0)
	}

	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 5, 0, false).
		AddItem(tableWrapper, 0, 1, true)

	return layout, settingsView, keymapView
}
func CreateLayout(downloads []api.Download, concurrency int, maxDownloadSpeed int) (*tview.Flex, *tview.TextView, *tview.TextView) {
	table := NewDownloadsTable(downloads)
	return CreateLayoutWithTable(table, downloads, concurrency, maxDownloadSpeed)
}

func UpdateSettingsView(settingsView *tview.TextView, downloads []api.Download, concurrency int, maxDownloadSpeed int) {
	settingsText := fmt.Sprintf("[::b]SETTINGS[::-]\n"+
		"Concurrency:     %d\n"+
		"Max Speed:       %d KB/s\n"+
		"Total Downloads: %d\n"+
		"Active:          %d\n"+
		"Completed:       %d",
		concurrency,
		maxDownloadSpeed,
		len(downloads),
		countActiveDownloads(downloads),
		countCompletedDownloads(downloads))

	settingsView.SetText(settingsText)
}

func countActiveDownloads(downloads []api.Download) int {
	count := 0
	for _, download := range downloads {
		if download.Status == "in_progress" {
			count++
		}
	}
	return count
}

func countCompletedDownloads(downloads []api.Download) int {
	count := 0
	for _, download := range downloads {
		if download.Status == "completed" {
			count++
		}
	}
	return count
}
