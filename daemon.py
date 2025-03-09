import atexit
import logging
import os
import re
import shutil
import subprocess
import threading
import time
import traceback
from datetime import datetime, timedelta, timezone
from queue import Empty, Queue

import requests

from config import get_config
from logger_config import setup_logger
from models import DaemonSettings, Download, Session

config = get_config()
logger = setup_logger(
    "aria2_daemon", getattr(logging, config["log_level"].upper(), logging.INFO)
)

# Default aria2 RPC settings
ARIA2_RPC_URL = config.get("aria2", {}).get(
    "rpc_url", "http://localhost:6800/jsonrpc"
)

# Get temporary download directory from config
TMP_DOWNLOAD_PATH = config["daemon"].get("temporary_download_directory")
if TMP_DOWNLOAD_PATH:
    os.makedirs(TMP_DOWNLOAD_PATH, exist_ok=True)


def parse_duration(duration_str):
    """Parse a duration string into a timedelta object.

    Supports formats like "1d", "2h", "30m", etc.
    Examples:
    - "1d": 1 day
    - "12h": 12 hours
    - "30m": 30 minutes
    - "1d12h": 1 day and 12 hours
    """
    if not duration_str:
        return timedelta(days=1)

    units = {"d": 86400, "h": 3600, "m": 60}
    pattern = re.findall(r"(\d+)([dhm])", duration_str)
    return timedelta(
        seconds=sum(int(value) * units[unit] for value, unit in pattern)
        if pattern
        else units["d"]
    )


def launch_cmd(command, url):
    """Run a command to map a URL, similar to the original daemon."""
    proc = subprocess.run(
        command + " " + url, capture_output=True, text=True, shell=True
    )
    if proc.returncode != 0:
        logger.error(
            "Error using %s to map url %s. Error: %s ",
            command,
            url,
            proc.stdout + proc.stderr,
        )
    return proc.returncode, proc.stdout + proc.stderr


def is_aria2c_running():
    """Check if aria2c RPC server is already running."""
    try:
        response = requests.post(
            ARIA2_RPC_URL,
            json={
                "jsonrpc": "2.0",
                "id": "check",
                "method": "aria2.getGlobalStat",
                "params": [],
            },
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
        logger.info(
            f"Initialized Aria2 JSON-RPC client connecting to {rpc_url}"
        )

    def _call_method(self, method, params=None):
        """Make a JSON-RPC call to aria2."""
        if params is None:
            params = []

        self.request_id += 1
        payload = {
            "jsonrpc": "2.0",
            "id": str(self.request_id),
            "method": method,
            "params": params,
        }

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


def handle_download_with_aria2(download_id, url, directory, move_queue=None):
    """Process a download using aria2 via JSON-RPC.

    Args:
        download_id: The ID of the download record
        url: The URL to download
        directory: The destination directory
        move_queue: Optional Queue to add move tasks (for sequential processing)
    """
    thread_session = Session()
    aria2_client = Aria2JsonRPC()
    logger.info(f"Starting aria2 download: {url} to {directory}")

    download_record = thread_session.get(Download, download_id)
    assert download_record, f"Download record with ID {download_id} not found"

    # aria2 GID to track the download
    gid = None
    download_path = directory
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
            if (
                filename is None
                and "files" in status_response
                and status_response["files"]
            ):
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
                # If using temporary download path, mark as waiting for move
                # Otherwise, mark as completed immediately
                if TMP_DOWNLOAD_PATH:
                    download_record.status = "waiting_for_move"
                else:
                    download_record.status = "completed"
                download_record.progress = "100%"
                download_record.speed = (
                    f"{total_size / (time.time() - start_time) / 1024:.2f} KB/s"
                )
            elif status == "error":
                completed = True
                download_record.status = "failed"
                download_record.error = status_response.get(
                    "errorMessage", "Unknown error"
                )
            elif status == "active":
                pass
            elif status == "paused":
                download_record.status = "paused"
            elif status == "waiting":
                download_record.status = "waiting"

            thread_session.commit()

            # Don't poll too frequently
            time.sleep(1)

        # Handle file movement from temporary directory if needed
        if status == "complete" and TMP_DOWNLOAD_PATH and filename:
            tmp_file_path = os.path.join(TMP_DOWNLOAD_PATH, filename)
            final_file_path = os.path.join(directory, filename)

            if os.path.exists(tmp_file_path):
                if move_queue is not None:
                    # Add to move queue for sequential processing
                    logger.info(
                        f"Adding move task to queue: {tmp_file_path} -> {final_file_path}"
                    )
                    task = MoveTask(tmp_file_path, final_file_path, download_id)
                    move_queue.put(task)
                else:
                    # Fallback to direct move if no queue provided
                    logger.info(
                        f"No move queue provided, doing direct move: {tmp_file_path} -> {final_file_path}"
                    )
                    shutil.move(tmp_file_path, final_file_path)
                    download_record.status = "completed"
                    thread_session.commit()
            else:
                logger.warning(f"Expected file not found at {tmp_file_path}")
                download_record.status = "failed"
                download_record.error = f"File not found at {tmp_file_path}"
                thread_session.commit()

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
                logger.error(
                    f"Error removing failed download from aria2: {remove_error}"
                )

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
        self.config = get_config()
        # Initialize the move processor
        self.move_processor = MoveProcessor()
        self.move_processor.start()
        logger.info("Initialized Aria2DownloadDaemon with MoveProcessor")

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
                    logger.info(
                        "aria2c RPC server successfully started and verified"
                    )
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

    def cleanup_old_downloads(self):
        """Delete downloads that are older than the configured expiration time."""
        try:
            expire_downloads_str = self.config["daemon"].get(
                "expire_downloads", "1d"
            )
            expire_duration = parse_duration(expire_downloads_str)

            cutoff_time = datetime.now(timezone.utc) - expire_duration

            old_downloads = (
                self.session.query(Download)
                .filter(Download.date_added < cutoff_time)
                .all()
            )

            if old_downloads:
                logger.info(
                    f"Found {len(old_downloads)} downloads older than {expire_downloads_str}"
                )

                for download in old_downloads:
                    logger.info(
                        f"Deleting old download: ID={download.id}, URL={download.url}, Added={download.date_added}"
                    )

                    if (
                        download.status in ["active", "waiting", "paused"]
                        and download.gid
                    ):
                        try:
                            self.aria2_client.remove(download.gid)
                            logger.info(
                                f"Removed active download from aria2: GID={download.gid}"
                            )
                        except Exception as e:
                            logger.warning(
                                f"Error removing download from aria2: {e}"
                            )

                    self.session.delete(download)

                self.session.commit()
                logger.info(
                    f"Successfully deleted {len(old_downloads)} old downloads"
                )
            else:
                logger.debug(
                    f"No downloads found older than {expire_downloads_str}"
                )

        except Exception as e:
            logger.error(f"Error in cleanup_old_downloads: {e}")
            logger.error(traceback.format_exc())
            self.session.rollback()

    def run(self):
        try:
            # Start aria2c if it's not already running
            self.aria2c_process = self.start_aria2c()

            # Register cleanup for this specific process
            if self.aria2c_process:
                # Create a closure to capture the current process
                def cleanup_this_process():
                    if self.aria2c_process:
                        logger.info(
                            f"Cleaning up aria2c process {self.aria2c_process.pid}"
                        )
                        self.cleanup_aria2c()

                # Register the cleanup function to run on exit
                atexit.register(cleanup_this_process)

            # If we couldn't start aria2c and it's not already running, exit
            if not self.aria2c_process and not is_aria2c_running():
                logger.error(
                    "Failed to ensure aria2c is running. Daemon will not start."
                )
                return

            # Initialize last cleanup time
            last_cleanup_time = datetime.now()

            while self.running:
                pending_downloads = (
                    self.session.query(Download)
                    .filter_by(status="pending")
                    .all()
                )
                daemon_settings = (
                    self.session.query(DaemonSettings).filter_by(id=1).first()
                )
                concurrency = (
                    daemon_settings.concurrency if daemon_settings else 1
                )

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
                    # Pass the move queue to the download handler
                    thread = threading.Thread(
                        target=handle_download_with_aria2,
                        args=(
                            download.id,
                            download.url,
                            download.directory,
                            self.move_processor.queue,
                        ),
                    )
                    thread.start()
                    active_threads.append(thread)

                # Check if it's time to clean up old downloads (every hour)
                current_time = datetime.now()
                if (
                    current_time - last_cleanup_time
                ).total_seconds() >= 3600:  # 1 hour in seconds
                    logger.info("Running cleanup of old downloads...")
                    self.cleanup_old_downloads()
                    last_cleanup_time = current_time

                # Sleep to avoid high CPU usage
                time.sleep(1)

        except Exception as e:
            logger.error(f"Error in Aria2DownloadDaemon: {e}")
            logger.error(traceback.format_exc())

        finally:
            self.session.close()
            # Clean up aria2c process when the thread exits
            self.cleanup_aria2c()
            # Stop the move processor
            if hasattr(self, "move_processor"):
                self.move_processor.stop()

    def stop(self):
        self.running = False
        self.move_processor.stop()
        # Cleanup will happen in the finally block of run()


class MoveTask:
    """Represents a task to move a file."""

    def __init__(self, source, destination, download_id):
        self.source = source
        self.destination = destination
        self.download_id = download_id


class MoveProcessor(threading.Thread):
    """Thread that handles moving files sequentially."""

    def __init__(self):
        super().__init__(daemon=True)
        self.queue = Queue()
        self.running = True
        self.session = Session()
        logger.info("Initialized MoveProcessor")

    def run(self):
        """Process move tasks from the queue sequentially."""
        logger.info("Starting move processor thread")
        while self.running:
            try:
                # Get a task from the queue, waiting for up to 1 second
                # This allows the thread to check self.running periodically
                try:
                    task = self.queue.get(timeout=1)
                except Empty:
                    continue

                # Process the task
                logger.info(
                    f"Processing move task: {task.source} -> {task.destination}"
                )
                try:
                    if os.path.exists(task.source):
                        # Create the destination directory if it doesn't exist
                        os.makedirs(
                            os.path.dirname(task.destination), exist_ok=True
                        )

                        # Perform the move
                        shutil.move(task.source, task.destination)
                        logger.info(
                            f"Successfully moved file: {task.source} -> {task.destination}"
                        )

                        # Update the database record if needed
                        download = self.session.get(Download, task.download_id)
                        if download:
                            download.status = "completed"
                            self.session.commit()
                    else:
                        logger.warning(f"Source file not found: {task.source}")
                except Exception as e:
                    logger.error(f"Error moving file: {str(e)}")
                    logger.error(traceback.format_exc())

                # Mark the task as done
                self.queue.task_done()

            except Exception as e:
                logger.error(f"Error in move processor: {str(e)}")
                logger.error(traceback.format_exc())

        logger.info("Move processor thread stopped")
        self.session.close()

    def stop(self):
        """Stop the move processor thread."""
        self.running = False
        logger.info("Stopping move processor thread")


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
