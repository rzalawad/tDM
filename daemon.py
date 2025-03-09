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

from models import DaemonSettings, Download, get_session

logger = logging.getLogger(__name__)

ARIA2_RPC_URL = "http://localhost:6800/jsonrpc"


def parse_duration(duration_str):
    """Parse a duration string into a timedelta object.
    Supports formats like "1d", "2h", "30m", etc.
    Examples: "1d": 1 day, "12h": 12 hours, "30m": 30 minutes, "1d12h": 1 day and 12 hours
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
    try:
        cmd_parts = command.split()
        cmd_parts.append(url)
        process = subprocess.Popen(
            cmd_parts, stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
        stdout, stderr = process.communicate()
        if process.returncode != 0:
            logger.error(f"Command failed: {stderr.decode()}")
            return None
        return stdout.decode().strip()
    except Exception as e:
        logger.error(f"Error executing command: {e}")
        return None


def is_aria2c_running():
    try:
        client = Aria2JsonRPC()
        client.get_global_stat()
        return True
    except Exception:
        return False


class Aria2JsonRPC:
    def __init__(self, rpc_url=ARIA2_RPC_URL):
        self.rpc_url = rpc_url

    def _call_method(self, method, params=None):
        """Make a JSON-RPC call to aria2c."""
        if params is None:
            params = []

        payload = {
            "jsonrpc": "2.0",
            "id": "aria2downloader",
            "method": f"aria2.{method}",
            "params": params,
        }

        try:
            response = requests.post(self.rpc_url, json=payload)
            response.raise_for_status()
            result = response.json()

            if "error" in result:
                logger.error(f"Aria2 RPC error: {result['error']}")
                raise Exception(f"Aria2 RPC error: {result['error']}")

            return result.get("result")
        except requests.RequestException as e:
            logger.error(f"Aria2 RPC request failed: {e}")
            raise

    def add_uri(self, uri, options=None):
        if options is None:
            options = {}
        return self._call_method("addUri", [[uri], options])

    def get_status(self, gid):
        return self._call_method("tellStatus", [gid])

    def get_global_stat(self):
        return self._call_method("getGlobalStat")

    def pause(self, gid):
        return self._call_method("pause", [gid])

    def unpause(self, gid):
        return self._call_method("unpause", [gid])

    def remove(self, gid):
        return self._call_method("remove", [gid])


def handle_download_with_aria2(
    download_id,
    url,
    directory,
    move_queue=None,
    tmp_download_path=None,
    mapper=None,
):
    aria2_client = Aria2JsonRPC()
    logger.info(f"Starting aria2 download: {url} to {directory}")

    with get_session() as thread_session:
        download_record = thread_session.get(Download, download_id)
        assert (
            download_record
        ), f"Download record with ID {download_id} not found"

        gid = None
        download_path = directory
        filename = None

        try:
            if mapper:
                for pattern, map_program in mapper.items():
                    if pattern in url:
                        logger.info(f"Mapping URL {url} with {map_program}")
                        mapped_url = launch_cmd(map_program, url)
                        if not mapped_url:
                            download_record.error = "URL mapping failed"
                            download_record.status = "failed"
                            return
                        else:
                            url = mapped_url
                            logger.info(f"URL mapped to {url}")
                        break

            if tmp_download_path:
                download_path = tmp_download_path

            options = {
                "dir": download_path,
                "continue": "true",
                "max-connection-per-server": "10",
            }

            gid = aria2_client.add_uri(url, options)
            download_record.status = "in_progress"
            download_record.error = None
            download_record.gid = gid

            completed = False
            start_time = time.time()
            while not completed:
                time.sleep(2)

                try:
                    status = aria2_client.get_status(gid)

                    if not status:
                        logger.error(f"Failed to get status for GID {gid}")
                        download_record.status = "failed"
                        download_record.error = "Failed to get download status"
                        break

                    current_status = status.get("status", "").lower()
                    download_record.status = current_status

                    if not filename and "files" in status and status["files"]:
                        path = status["files"][0].get("path", "")
                        if path:
                            filename = os.path.basename(path)

                    total_length = int(status.get("totalLength", "0"))
                    completed_length = int(status.get("completedLength", "0"))
                    download_speed = int(status.get("downloadSpeed", "0"))

                    if total_length > 0:
                        progress = (completed_length / total_length) * 100
                        download_record.progress = f"{progress:.1f}%"

                    download_record.total_size = total_length
                    download_record.downloaded = completed_length
                    download_record.speed = (
                        f"{download_speed / 1024:.1f} KB/s"
                        if download_speed > 0
                        else None
                    )

                    if current_status == "complete":
                        download_record.status = "completed"
                        completed = True

                        if tmp_download_path and move_queue and filename:
                            source = os.path.join(download_path, filename)
                            destination = os.path.join(directory, filename)
                            move_queue.put(
                                MoveTask(source, destination, download_id)
                            )
                            logger.info(
                                f"Queued move task: {source} -> {destination}"
                            )

                    elif current_status == "error":
                        error_msg = status.get("errorMessage", "Unknown error")
                        download_record.error = error_msg
                        download_record.status = "failed"
                        completed = True

                    elif time.time() - start_time > 3600:
                        download_record.error = "Download timed out"
                        download_record.status = "failed"
                        completed = True

                        try:
                            aria2_client.remove(gid)
                        except Exception as e:
                            logger.error(
                                f"Error removing timed out download: {e}"
                            )

                except Exception as e:
                    logger.error(f"Error updating download status: {e}")
                    time.sleep(5)

        except Exception as e:
            logger.error(f"Error in download process: {e}")
            logger.error(traceback.format_exc())
            download_record.error = str(e)
            download_record.status = "failed"

            if gid:
                try:
                    aria2_client.remove(gid)
                except Exception as remove_error:
                    logger.error(
                        f"Error removing failed download from aria2: {remove_error}"
                    )

    logger.info(f"Download {download_record.status}: {url}")


class Aria2DownloadDaemon(threading.Thread):
    def __init__(self, daemon_config):
        super().__init__()
        self.running = True
        self.aria2_client = Aria2JsonRPC()
        self.aria2c_process = None
        self.config = daemon_config

        if self.config.temporary_download_directory:
            os.makedirs(self.config.temporary_download_directory, exist_ok=True)

        self.move_processor = MoveProcessor()
        self.move_processor.start()

        logger.info("Initialized Aria2DownloadDaemon and MoveProcessor")

    def start_aria2c(self):
        logger.info("Starting aria2c RPC server...")

        cmd = [
            "aria2c",
            "--enable-rpc",
            "--rpc-listen-all=true",
            "--rpc-allow-origin-all",
        ]

        try:
            process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                preexec_fn=os.setpgrp if os.name != "nt" else None,
            )

            logger.info(f"Started aria2c RPC server with PID {process.pid}")
            time.sleep(1)

            if process.poll() is not None:
                stdout, stderr = process.communicate()
                logger.error(f"aria2c failed to start: {stderr.decode()}")
                return None

            for i in range(5):
                if is_aria2c_running():
                    logger.info(
                        "aria2c RPC server successfully started and verified"
                    )
                    return process
                else:
                    time.sleep(0.5 * (i + 1))

            logger.error("aria2c started but RPC server failed to respond")
            process.terminate()
            return None

        except Exception as e:
            logger.error(f"Failed to start aria2c: {e}")
            return None

    def cleanup_aria2c(self):
        if self.aria2c_process is not None:
            logger.info("Terminating aria2c process...")
            try:
                self.aria2c_process.terminate()
                self.aria2c_process.wait(timeout=5)
                logger.info("aria2c process terminated successfully")
            except Exception as e:
                logger.error(f"Error terminating aria2c process: {e}")
                try:
                    self.aria2c_process.kill()
                    logger.info("aria2c process forcefully killed")
                except Exception as ke:
                    logger.error(f"Failed to kill aria2c process: {ke}")
            finally:
                self.aria2c_process = None

    def cleanup_old_downloads(self):
        try:
            expire_downloads_str = self.config.expire_downloads
            expire_duration = parse_duration(expire_downloads_str)
            cutoff_date = datetime.now(timezone.utc) - expire_duration

            with get_session() as session:
                old_downloads = (
                    session.query(Download)
                    .filter(Download.date_added < cutoff_date)
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
                            download.status
                            in ["in_progress", "waiting", "paused"]
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

                        session.delete(download)

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

    def run(self):
        try:
            self.aria2c_process = self.start_aria2c()

            if self.aria2c_process:

                def cleanup_this_process():
                    if self.aria2c_process:
                        logger.info(
                            f"Cleaning up aria2c process {self.aria2c_process.pid}"
                        )
                        self.cleanup_aria2c()

                atexit.register(cleanup_this_process)

            if not self.aria2c_process and not is_aria2c_running():
                logger.error(
                    "Failed to ensure aria2c is running. Daemon will not start."
                )
                return

            last_cleanup_time = datetime.now()

            while self.running:
                with get_session() as session:
                    pending_downloads = (
                        session.query(Download)
                        .filter_by(status="pending")
                        .all()
                    )

                    try:
                        daemon_settings = session.query(DaemonSettings).first()
                        concurrency = (
                            daemon_settings.concurrency
                            if daemon_settings
                            else self.config.concurrency
                        )
                    except Exception as e:
                        logger.error(f"Error fetching daemon settings: {e}")
                        concurrency = self.config.concurrency

                try:
                    stats = self.aria2_client.get_global_stat()
                    active_count = int(stats.get("numActive", 0))
                    available_slots = max(0, concurrency - active_count)
                except Exception as e:
                    logger.error(f"Error getting global stats: {e}")
                    available_slots = 0

                for download in pending_downloads[:available_slots]:
                    threading.Thread(
                        target=handle_download_with_aria2,
                        args=(
                            download.id,
                            download.url,
                            download.directory,
                            self.move_processor.queue,
                        ),
                        kwargs={
                            "tmp_download_path": self.config.temporary_download_directory,
                            "mapper": self.config.mapper,
                        },
                        daemon=True,
                    ).start()

                if (datetime.now() - last_cleanup_time).total_seconds() >= 3600:
                    logger.info("Running cleanup of old downloads...")
                    self.cleanup_old_downloads()
                    last_cleanup_time = datetime.now()

                time.sleep(5)

        except Exception as e:
            logger.error(f"Error in Aria2DownloadDaemon: {e}")
            logger.error(traceback.format_exc())

        finally:
            self.cleanup_aria2c()
            self.move_processor.stop()
            self.move_processor.join()

    def stop(self):
        self.running = False


class MoveTask:
    def __init__(self, source, destination, download_id):
        self.source = source
        self.destination = destination
        self.download_id = download_id


class MoveProcessor(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.queue = Queue()
        self.running = True
        logger.info("Initialized MoveProcessor")

    def run(self):
        logger.info("Starting move processor thread")
        while self.running:
            try:
                try:
                    task = self.queue.get(timeout=1)
                except Empty:
                    continue

                logger.info(
                    f"Processing move task: {task.source} -> {task.destination}"
                )
                try:
                    if os.path.exists(task.source):
                        os.makedirs(
                            os.path.dirname(task.destination), exist_ok=True
                        )
                        shutil.move(task.source, task.destination)
                        logger.info(
                            f"Successfully moved file: {task.source} -> {task.destination}"
                        )

                        with get_session() as session:
                            download = session.get(Download, task.download_id)
                            if download:
                                download.status = "completed"

                    else:
                        logger.warning(f"Source file not found: {task.source}")
                except Exception as e:
                    logger.error(f"Error moving file: {e}")
                    logger.error(traceback.format_exc())

                self.queue.task_done()

            except Exception as e:
                logger.error(f"Error in move processor: {e}")
                logger.error(traceback.format_exc())

        logger.info("Move processor thread stopped")

    def stop(self):
        self.running = False
        logger.info("Stopping move processor thread")
