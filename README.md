# tDM - Terminal Download Manager

A robust, feature-rich terminal-based download manager designed to help create your file archives.

## Overview

tDM provides a comprehensive solution for managing downloads through a terminal interface. It combines a backend server with an intuitive terminal UI, allowing you to monitor and control downloads efficiently. The project is particularly useful for managing large archives and handling various download sources.

## Features

- **Multi-Archive Handling**: Download and automatically unpack multiple archives
- **Smart Cleanup**: Configurable automatic cleanup of completed downloads
- **Safe Downloads**: Uses a temporary directory before moving to final destination
- **Dynamic Concurrency**: Adjust download concurrency in real-time
- **URL Remapping**: Map undownloadable URLs to downloadable ones (debrid service integration)
- **Group Downloads**: Manage downloads as logical groups
- **Post-Download Actions**: Automatically extract archives (unrar, 7z, etc.)
- **User Interface**: Terminal-based UI for monitoring and control

## Architecture

tDM consists of three main components:

- **Server**: A Flask-based API server that manages download requests and tracks progress using SQLAlchemy
- **Daemon**: A background process that handles the actual file downloads, supporting multiple download methods
- **Client**: A Go-based terminal UI built with tview, providing both interactive (TUI) and command-line (CLI) interfaces

## Installation

*Coming soon*

## Configuration

See the example configuration file at [config.example.yaml](./config.example.yaml) for detailed options.

The configuration covers:
- Server settings (host, port)
- Daemon settings (concurrency, expiration, download directories)
- URL mappers for premium download services
- Aria2 downloader options
- Logging configuration

## Usage

*Coming soon*

## Development

### Project Structure
```
tDM/
├── server/         # Python-based backend
│   ├── main.py     # Server entry point
│   └── tdm/        # Core modules
│       ├── api/    # REST API endpoints
│       ├── core/   # Configuration and models
│       └── daemon/ # Download management
├── client/         # Go-based frontend
│   ├── cmd/        # Command implementations
│   └── internal/   # UI and API communication
└── config.example.yaml  # Configuration template
```

## Roadmap

- [x] Manage group downloads
- [x] Allow group downloads + post-processing (unrar, 7z, etc)
- [x] Delete downloads from UI
- [x] Handle torrents
- [ ] Add unit tests for server, daemon, and client components
- [ ] Fix Aria2 stability issues (API calls occasionally get stuck)
- [ ] Handle cleaner exit behavior when aria2c is downloading
- [ ] Support downloads with multiple files/URIs

## License

*License information*

## Contributing

*Contribution guidelines*

