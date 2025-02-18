import argparse
import sys
import time

import requests
from readchar import key, readkey
from rich.console import Console
from rich.layout import Layout
from rich.live import Live
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

API_URL = "http://localhost:54759"

SELECTED_ROW = 0
CONCURRENCY_SETTINGS = {"max_concurrent_downloads": 3, "max_download_speed": 0}


def submit_download(url, directory="."):
    try:
        response = requests.post(
            f"{API_URL}/download", json={"url": url, "directory": directory}
        )
        response.raise_for_status()
        print(f"Download request for {url} submitted successfully.")
    except requests.exceptions.RequestException as e:
        print(f"Error submitting download request: {e}")


def parse_arguments():
    parser = argparse.ArgumentParser(description="Download Manager Client")
    subparsers = parser.add_subparsers(dest="command")
    add_parser = subparsers.add_parser("add", help="Add a new download")
    add_parser.add_argument("url", type=str, help="URL of the file to download")
    add_parser.add_argument(
        "directory",
        type=str,
        nargs="?",
        default=".",
        help="Destination directory (optional)",
    )
    return parser.parse_args()


def fetch_downloads():
    try:
        response = requests.get(f"{API_URL}/downloads")
        response.raise_for_status()
        return response.json(), response.status_code, ""
    except requests.exceptions.RequestException as e:
        return (
            None,
            None,
            "Could not connect to the server. Please ensure the server is running.",
        )


def create_layout() -> Layout:
    layout = Layout()
    layout.split_column(
        Layout(name="header", size=3),
        Layout(name="main", ratio=1),
        Layout(name="footer", size=3),
    )
    return layout


def render_downloads_table(downloads, live) -> Table:
    table = Table(
        title="[bold blue]Current Downloads[/bold blue]",
        expand=True,
        box=box.DOUBLE_EDGE,
        style="bright_black",
    )
    table.add_column("ID", justify="right", style="bold cyan", no_wrap=True)
    table.add_column("URL", style="magenta")
    table.add_column("Status", style="green")
    table.add_column("Directory", style="yellow")
    table.add_column("Speed", style="blue")
    table.add_column("Downloaded", style="white")
    table.add_column("Total Size", style="white")
    table.add_column("Date Added", style="white")
    table.add_column("Progress", style="white")
    print(live.console.log(downloads))
    for download in downloads:
        downloaded_str = (
            f"{download['downloaded'] / (1024**2):.2f} MB"
            if download["downloaded"]
            else "0 MB"
        )
        total_size_str = (
            f"{download['total_size'] / (1024**2):.2f} MB"
            if download["total_size"]
            else "Unknown"
        )
        table.add_row(
            str(download["id"]),
            download["url"],
            download["status"],
            download["directory"],
            download["speed"] or "N/A",
            downloaded_str,
            total_size_str,
            download["date_added"],
            download["progress"] or "0%",
        )
    return table


def display_ui():
    layout = create_layout()
    with Live(layout, screen=True) as live:
        i = 0
        while True:
            result = fetch_downloads()
            print(live.console.log(result))
            layout["footer"].update(str(result) + f" {i}")
            layout["header"].update(Panel("[bold]Download Manager[/bold]"))
            downloads, status_code, error_message = result
            if error_message:
                layout["main"].update(
                    Panel(f"[bold red]Error:[/bold red] {error_message}")
                )
            elif status_code != 200:
                layout["main"].update(
                    Panel(
                        f"[bold red]Error:[/bold red] Server returned status code {status_code}."
                    )
                )
            else:
                layout["main"].update(render_downloads_table(downloads, live))
            i += 1


if __name__ == "__main__":
    args = parse_arguments()
    if args.command == "add":
        submit_download(args.url, args.directory)
    else:
        display_ui()
