# Terminal-Based Download Manager

## Project Goal
The goal of this project is to create a terminal-based application with a client-server architecture that manages file downloads. The application includes a server that handles download requests, a daemon that manages download concurrency, and a client that provides a terminal UI for monitoring and controlling downloads.

## Project Structure
- **Server**: A Flask server that accepts download requests and manages a database using SQLAlchemy to track downloads. It includes endpoints for managing downloads and updating concurrency settings.
- **Daemon**: A background process that handles file downloads using the `requests` library, updating download speed, progress, and status in real-time.
- **Python Client**: A terminal-based UI using the `rich` library to display current downloads, their status, speed, progress, and additional details like file size and date added.
- **Go Client**: A terminal-based UI built with `tview` that allows users to monitor downloads and update concurrency settings via a TUI or CLI.

Two clients exist because I started with the python client but realized the lack of interactive
support in `rich` library

## Current Status
- **Server**: Fully functional with endpoints for managing downloads and concurrency settings.
- **Daemon**: Operational, managing download concurrency and real-time updates.
- **Python Client**: Provides a basic UI for monitoring downloads.
- **Go Client**: Enhanced with features for setting concurrency and displaying current settings.

## TODO
- Manage group downloads
- Add unit tests for server, daemon, and client components.
- Allow group downloads + <abstract group task> (unrar, 7z, etc)
- Allow cleaner exit behavior when aria2c is downloading
- Delete downloads from UI
- Handle downloads with multiple files / uris
- Handle torrents
