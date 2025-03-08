import atexit
import logging
import os
import shutil
import subprocess
import threading
import time
import traceback

import requests

from config import get_config
from logger_config import setup_logger
from models import DaemonSettings, Download, Session

config = get_config()
logger = setup_logger("aria2_daemon", getattr(logging, config["log_level"].upper(), logging.INFO))

# Default aria2 RPC settings
ARIA2_RPC_URL = config.get("aria2", {}).get("rpc_url", "http://localhost:6800/jsonrpc")

# Get temporary download directory from config
TMP_DOWNLOAD_PATH = config["daemon"].get("temporary_download_directory")
if TMP_DOWNLOAD_PATH:
    os.makedirs(TMP_DOWNLOAD_PATH, exist_ok=True)


def launch_cmd(command, url):
    """Run a command to map a URL, similar to the original daemon."""
    proc = subprocess.run(command + " " + url, capture_output=True, text=True, shell=True)
    if proc.returncode != 0:
        logger.error(
            "Error using %s to map url %s. Error: %s ", command, url, proc.stdout + proc.stderr
        )
    return proc.returncode, proc.stdout + proc.stderr


def is_aria2c_running():
    """Check if aria2c RPC server is already running."""
    try:
        response = requests.post(
            ARIA2_RPC_URL,
            json={"jsonrpc": "2.0", "id": "check", "method": "aria2.getGlobalStat", "params": []},
        )
        if response.status_code == 200:
            logger.info("aria2c RPC server is already running")
            return True
    except requests.RequestException:
        pass
    return False


class Aria2JsonRPC:
    """Simple client for aria2's JSON-RPC interface."""

    def __init__(self, rpc_url=ARIA2_RPC_URL):
        self.rpc_url = rpc_url
        self.request_id = 0
        logger.info(f"Initialized Aria2 JSON-RPC client connecting to {rpc_url}")

    def _call_method(self, method, params=None):
        """Make a JSON-RPC call to aria2."""
        if params is None:
            params = []

        self.request_id += 1
        payload = {"jsonrpc": "2.0", "id": str(self.request_id), "method": method, "params": params}

        response = requests.post(self.rpc_url, json=payload)
        response_data = response.json()

        if "error" in response_data:
            error_msg = f"JSON-RPC error: {response_data['error']['message']}"
            logger.error(error_msg)
            raise Exception(error_msg)

        return response_data.get("result")

    def add_uri(self, uri, options=None):
        """Add a URI to download."""
        params = [[uri]]  # First param is array of URIs
        if options:
            params.append(options)  # Second param is options
        return self._call_method("aria2.addUri", params)

    def get_status(self, gid):
        """Get the status of a download."""
        return self._call_method("aria2.tellStatus", [gid])

    def get_global_stat(self):
        """Get global statistics."""
        return self._call_method("aria2.getGlobalStat")

    def pause(self, gid):
        """Pause a download."""
        return self._call_method("aria2.pause", [gid])

    def unpause(self, gid):
        """Resume a paused download."""
        return self._call_method("aria2.unpause", [gid])

    def remove(self, gid):
        """Remove a download."""
        return self._call_method("aria2.remove", [gid])


def handle_download_with_aria2(download_id, url, directory):
    """Process a download using aria2 via JSON-RPC."""
    thread_session = Session()
    aria2_client = Aria2JsonRPC()
    logger.info(f"Starting aria2 download: {url} to {directory}")

    download_record = thread_session.query(Download).get(download_id)
    assert download_record, f"Download record with ID {download_id} not found"

    # aria2 GID to track the download
    gid = None
    download_path = directory
    final_path = directory
    filename = None

    try:
        # Check if URL needs mapping using configured mappers
        for pattern, map_program in config["daemon"].get("mapper", {}).items():
            if pattern in url:
                logger.info(f"Mapping URL {url} with {map_program}")
                retcode, retval = launch_cmd(map_program, url)
                if retcode != 0:
                    download_record.error = retval
                    download_record.status = "failed"
                    thread_session.commit()
                    return
                else:
                    url = retval.strip()
                    logger.info(f"URL mapped to {url}")
                break

        # Set download location - use temporary if configured
        if TMP_DOWNLOAD_PATH:
            # We'll determine the actual filename after the download starts
            download_path = TMP_DOWNLOAD_PATH

        # Add the URL to aria2
        options = {
            "dir": download_path,
            "continue": "true",
            "max-connection-per-server": "10",
        }

        gid = aria2_client.add_uri(url, options)
        download_record.status = "in_progress"
        download_record.error = None  # Clear any previous errors
        download_record.gid = gid
        thread_session.commit()

        # Keep polling for status until complete
        completed = False
        start_time = time.time()
        while not completed:
            status_response = aria2_client.get_status(gid)

            # Update download record with current progress
            downloaded = int(status_response.get("completedLength", "0"))
            total_size = int(status_response.get("totalLength", "0"))

            download_record.downloaded = downloaded
            download_record.total_size = total_size

            # Get the filename from the first file (if available)
            if filename is None and "files" in status_response and status_response["files"]:
                file_info = status_response["files"][0]
                if "path" in file_info and file_info["path"]:
                    filename = os.path.basename(file_info["path"])
                    logger.info(f"Detected filename: {filename}")

            if total_size > 0:
                progress = (downloaded / total_size) * 100
                download_record.progress = f"{progress:.2f}%"

            # Update download speed
            download_speed = int(status_response.get("downloadSpeed", "0"))
            download_record.speed = f"{download_speed / 1024:.2f} KB/s"

            # Check status
            status = status_response.get("status", "")
            if status == "complete":
                completed = True
                download_record.status = "completed"
                download_record.progress = "100%"
                download_record.speed = f"{total_size / (time.time() - start_time) / 1024:.2f} KB/s"
            elif status == "error":
                completed = True
                download_record.status = "failed"
                download_record.error = status_response.get("errorMessage", "Unknown error")
            elif status == "active":
                # Still downloading
                pass
            elif status == "paused":
                # Paused
                download_record.status = "paused"
            elif status == "waiting":
                # Waiting to start
                download_record.status = "waiting"

            thread_session.commit()

            # Don't poll too frequently
            time.sleep(1)

        # Handle file movement from temporary directory if needed
        if status == "complete" and TMP_DOWNLOAD_PATH and filename:
            tmp_file_path = os.path.join(TMP_DOWNLOAD_PATH, filename)
            final_file_path = os.path.join(directory, filename)

            if os.path.exists(tmp_file_path):
                logger.info(f"Moving file from {tmp_file_path} to {final_file_path}")
                shutil.move(tmp_file_path, final_file_path)
            else:
                logger.warning(f"Expected file not found at {tmp_file_path}")

    except Exception as e:
        logger.error(f"Error during aria2 download of {url}: {str(e)}")
        error_traceback = traceback.format_exc()
        download_record.status = "failed"
        download_record.error = f"{str(e)}\n\nTraceback:\n{error_traceback}"
        thread_session.commit()

        # Try to remove the download from aria2 if we have a GID
        if gid:
            try:
                aria2_client.remove(gid)
            except Exception as remove_error:
                logger.error(f"Error removing failed download from aria2: {remove_error}")

    logger.info(f"Download {download_record.status}: {url}")
    thread_session.close()


class Aria2DownloadDaemon(threading.Thread):
    """Daemon thread that manages downloads through aria2."""

    def __init__(self):
        super().__init__()
        self.running = True
        self.session = Session()
        self.aria2_client = Aria2JsonRPC()
        self.aria2c_process = None
        logger.info("Initialized Aria2DownloadDaemon")

    def start_aria2c(self):
        """Start aria2c as a subprocess and return the process."""

        logger.info("Starting aria2c RPC server...")

        cmd = [
            "aria2c",
            "--enable-rpc",
            "--rpc-listen-all=true",
            "--rpc-allow-origin-all",
            # No daemon flag - we want to control the process lifecycle
        ]

        try:
            # Start aria2c as a child process
            process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                # Create a new process group for better control
                preexec_fn=os.setpgrp if os.name != "nt" else None,
            )

            logger.info(f"Started aria2c RPC server with PID {process.pid}")

            # Give it a moment to start up
            time.sleep(1)

            # Check if the process is still running
            if process.poll() is not None:
                stdout, stderr = process.communicate()
                logger.error(f"aria2c failed to start: {stderr.decode()}")
                return None

            # Verify it's responding to RPC with retries
            for i in range(5):  # Try a few times with backoff
                if is_aria2c_running():
                    logger.info("aria2c RPC server successfully started and verified")
                    return process
                else:
                    time.sleep(0.5 * (i + 1))  # Backoff delay

            # If we get here, verification failed
            logger.error("aria2c started but RPC server failed to respond")
            process.terminate()
            return None

        except Exception as e:
            logger.error(f"Failed to start aria2c: {e}")
            return None

    def cleanup_aria2c(self):
        """Terminate the aria2c process if it's running."""
        if self.aria2c_process is not None:
            logger.info("Terminating aria2c process...")
            try:
                # Send SIGTERM to the process
                self.aria2c_process.terminate()
                # Give it some time to shut down gracefully
                self.aria2c_process.wait(timeout=5)
                logger.info("aria2c process terminated successfully")
            except Exception as e:
                logger.error(f"Error terminating aria2c process: {e}")
                # Force kill if terminate didn't work
                try:
                    self.aria2c_process.kill()
                    logger.info("aria2c process forcefully killed")
                except Exception as ke:
                    logger.error(f"Failed to kill aria2c process: {ke}")
            finally:
                self.aria2c_process = None

    def run(self):
        try:
            # Start aria2c if it's not already running
            self.aria2c_process = self.start_aria2c()

            # Register cleanup for this specific process
            if self.aria2c_process:
                # Create a closure to capture the current process
                def cleanup_this_process():
                    if self.aria2c_process:
                        logger.info(f"Cleaning up aria2c process {self.aria2c_process.pid}")
                        self.cleanup_aria2c()

                # Register the cleanup function to run on exit
                atexit.register(cleanup_this_process)

            # If we couldn't start aria2c and it's not already running, exit
            if not self.aria2c_process and not is_aria2c_running():
                logger.error("Failed to ensure aria2c is running. Daemon will not start.")
                return

            while self.running:
                pending_downloads = self.session.query(Download).filter_by(status="pending").all()
                daemon_settings = self.session.query(DaemonSettings).filter_by(id=1).first()
                concurrency = daemon_settings.concurrency if daemon_settings else 1

                # Get current active downloads count from aria2c
                try:
                    global_stat = self.aria2_client.get_global_stat()
                    active_count = int(global_stat.get("numActive", "0"))
                    # Only start new downloads if we're below concurrency limit
                    available_slots = max(0, concurrency - active_count)
                except Exception as e:
                    logger.error(f"Error getting global stats: {e}")
                    available_slots = 0

                active_threads = []
                for download in pending_downloads[:available_slots]:
                    download.status = "submitted"
                    self.session.commit()
                    thread = threading.Thread(
                        target=handle_download_with_aria2,
                        args=(download.id, download.url, download.directory),
                    )
                    thread.start()
                    active_threads.append(thread)

                # Sleep to avoid high CPU usage
                time.sleep(1)

        except Exception as e:
            logger.error(f"Error in Aria2DownloadDaemon: {e}")
            logger.error(traceback.format_exc())

        finally:
            self.session.close()
            # Clean up aria2c process when the thread exits
            self.cleanup_aria2c()

    def stop(self):
        self.running = False
        # Cleanup will happen in the finally block of run()


if __name__ == "__main__":
    daemon = Aria2DownloadDaemon()
    daemon.start()

    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        logger.info("Shutting down Aria2DownloadDaemon...")
        daemon.stop()
        daemon.join()
        logger.info("Aria2DownloadDaemon stopped successfully")
