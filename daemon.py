import logging
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import traceback
from datetime import datetime, timedelta, timezone
from typing import List, Optional

import requests
from sqlalchemy.dialects.sqlite import insert

from config import DaemonConfig
from models import (
    TASK_TYPE_TO_STATUS,
    DaemonSettings,
    Download,
    Group,
    GroupStatus,
    Status,
    Task,
    TaskType,
    get_session,
    session_scope,
)

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
        command += f" {url}"
        process = subprocess.run(
            command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, shell=True
        )
        if process.returncode != 0:
            logger.error(f"Command failed: {process.stderr.decode()}")
            return (
                None,
                process.stdout.decode() + "\n" + process.stderr.decode(),
            )
        return process.stdout.decode().strip(), None
    except Exception as e:
        logger.error(f"Error executing command: {e}")
        return None, str(e) + "\n" + traceback.format_exc()


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
        logger.debug(
            "Making RPC call to %s with payload %s", self.rpc_url, payload
        )

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
    tmp_download_path=None,
    mapper=None,
    aria2_options={},
):
    aria2_client = Aria2JsonRPC()
    logger.info(f"Starting aria2 download: {url} to {directory}")

    with session_scope() as thread_session:
        download = thread_session.get(Download, download_id)
        assert download, f"Download record with ID {download_id} not found"

        gid = None
        download_path = directory
        filename = None

        try:
            if mapper:
                for pattern, map_program in mapper.items():
                    if pattern in url:
                        logger.info(f"Mapping URL {url} with {map_program}")
                        mapped_url, err = launch_cmd(map_program, url)
                        if err:
                            download.error = err
                            download.status = Status.FAILED
                            return
                        else:
                            url = mapped_url
                            logger.info(f"URL mapped to {url}")
                        break

            if tmp_download_path:
                download_path = tmp_download_path
            download_path = os.path.join(download_path, str(download.group_id))

            options = {
                "dir": download_path,
                "continue": "true",
                **aria2_options,
            }

            gid = aria2_client.add_uri(url, options)
            download.status = Status.DOWNLOADING
            download.error = None
            download.gid = gid

            completed = False
            start_time = time.time()
            while not completed:
                thread_session.commit()
                time.sleep(1)

                try:
                    status = aria2_client.get_status(gid)

                    if not status:
                        logger.error(f"Failed to get status for GID {gid}")
                        download.status = Status.FAILED
                        download.error = "Failed to get download status"
                        break

                    current_status = status.get("status", "").lower()

                    if not filename and "files" in status and status["files"]:
                        path = status["files"][0].get("path", "")
                        if path:
                            filename = os.path.basename(path)

                    total_length = int(status.get("totalLength", "0"))
                    completed_length = int(status.get("completedLength", "0"))
                    download_speed = int(status.get("downloadSpeed", "0"))

                    if total_length > 0:
                        progress = (completed_length / total_length) * 100
                        download.progress = f"{progress:.1f}%"

                    download.total_size = total_length
                    download.downloaded = completed_length
                    download.speed = (
                        f"{download_speed / 1024:.1f} KB/s"
                        if download_speed > 0
                        else None
                    )

                    if current_status == "complete":
                        # check if download is part of a group. Only the first download of a group should put a task in the work queue
                        group_downloads = (
                            thread_session.query(Download)
                            .filter_by(group_id=download.group_id)
                            .all()
                        )
                        group_downloads.sort(key=lambda x: x.id)

                        unpack_present = move_present = False
                        # order of this logic is important
                        if download.group.task == TaskType.UNPACK:
                            download.status = Status.UNPACKING

                            # Use insert with on_conflict_do_nothing for unique constraint
                            stmt = (
                                insert(Task)
                                .values(
                                    task_type=TaskType.UNPACK,
                                    group_id=download.group_id,
                                    status=Status.PENDING,
                                )
                                .on_conflict_do_nothing(
                                    index_elements=["group_id", "task_type"]
                                )
                            )
                            thread_session.execute(stmt)
                            thread_session.commit()

                            logger.info(
                                f"Created unpack task in database for group {download.group_id}"
                            )
                            unpack_present = True
                            download.group.status = GroupStatus.UNPACKING

                        if directory != download_path:
                            if not unpack_present:
                                download.status = Status.MOVING

                            # Use insert with on_conflict_do_nothing for unique constraint
                            stmt = (
                                insert(Task)
                                .values(
                                    task_type=TaskType.MOVE,
                                    group_id=download.group_id,
                                    status=Status.PENDING,
                                )
                                .on_conflict_do_nothing(
                                    index_elements=["group_id", "task_type"]
                                )
                            )
                            thread_session.execute(stmt)
                            thread_session.commit()

                            logger.info(
                                f"Created move task in database for group {download.group_id}"
                            )
                            move_present = True

                        if not unpack_present and not move_present:
                            download.status = Status.COMPLETED
                        completed = True
                        download.speed = f"{total_length / (time.time() - start_time) / 1024:.1f} KB/s"

                    elif current_status == "error":
                        error_msg = status.get("errorMessage", "Unknown error")
                        download.error = error_msg
                        download.status = Status.FAILED
                        completed = True

                    elif time.time() - start_time > 3600 * 24:
                        download.error = "Download timed out"
                        download.status = Status.FAILED
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
            download.error = str(e) + "\n" + traceback.format_exc()
            download.status = Status.FAILED

            if gid:
                try:
                    aria2_client.remove(gid)
                except Exception as remove_error:
                    logger.error(
                        f"Error removing failed download from aria2: {remove_error}"
                    )

        logger.info(f"Download {download.status}: {url}")


class Aria2DownloadDaemon(threading.Thread):
    def __init__(self, daemon_config: DaemonConfig):
        super().__init__()
        self.running = True
        self.aria2_client = Aria2JsonRPC()
        self.aria2c_process = None
        self.config = daemon_config

        if self.config.temporary_download_directory:
            os.makedirs(self.config.temporary_download_directory, exist_ok=True)

        self.work_processor = WorkProcessor(
            self.config.temporary_download_directory
        )
        self.work_processor.start()

        self.session = get_session()
        logger.info("Initialized Aria2DownloadDaemon and WorkProcessor")

    def start_aria2c(self):
        if is_aria2c_running():
            return
        cmd = self.config.aria2.build_command()
        try:
            _ = subprocess.run(
                cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True
            )
        except subprocess.CalledProcessError as e:
            logger.error(f"Failed to start aria2c: {e}")
            logger.error(f"Command: {e.cmd}")
            logger.error(f"Return code: {e.returncode}")
            logger.error(f"stdout: {e.stdout.decode() if e.stdout else ''}")
            logger.error(f"stderr: {e.stderr.decode() if e.stderr else ''}")
            sys.exit(1)
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

            with session_scope() as session:
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
                            in ["downloading", "waiting", "paused"]
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
            self.start_aria2c()

            last_cleanup_time = datetime.now()

            while self.running:
                self.session.expire_all()
                pending_downloads = (
                    self.session.query(Download)
                    .filter_by(status=Status.PENDING)
                    .all()
                )

                try:
                    daemon_settings = self.session.query(DaemonSettings).first()
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
                    download.status = Status.SUBMITTED
                    self.session.commit()
                    threading.Thread(
                        target=handle_download_with_aria2,
                        args=(
                            download.id,
                            download.url,
                            download.directory,
                        ),
                        kwargs={
                            "tmp_download_path": self.config.temporary_download_directory,
                            "mapper": self.config.mapper,
                            "aria2_options": self.config.aria2.download_options,
                        },
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
            self.work_processor.stop()
            self.work_processor.join()

    def stop(self):
        self.running = False


class WorkProcessor(threading.Thread):
    def __init__(self, temporary_download_directory: Optional[str] = None):
        super().__init__()
        self.running = True
        self.session = get_session()
        self.temporary_download_directory = temporary_download_directory
        logger.info("Initialized WorkProcessor")

    def perform_move_task(self, task: Task, downloads: List[Download]):
        try:
            # The source is the temporary download path
            if not downloads:
                logger.warning(
                    f"No downloads found for group ID: {task.group_id}"
                )
                return

            group = self.session.get(Group, task.group_id)

            # The source is the temporary download path
            # The destination is the final directory specified in the download
            if downloads and downloads[0]:
                source = os.path.join(
                    self.temporary_download_directory or downloads[0].directory,
                    str(task.group_id),
                )
                # Destination is the final directory specified in the download
                destination = downloads[0].directory
            else:
                error_msg = (
                    "Could not determine source or destination for move task"
                )
                logger.error(error_msg)
                group.error = error_msg
                group.status = GroupStatus.FAILED
                task.status = Status.FAILED
                task.error = error_msg
                self.session.commit()
                return

            if not os.path.exists(source):
                error_msg = f"Source file not found: {source}"
                logger.warning(error_msg)
                group.error = error_msg
                group.status = GroupStatus.FAILED
                task.status = Status.FAILED
                task.error = error_msg
                self.session.commit()
                return

            os.makedirs(os.path.dirname(destination), exist_ok=True)
            shutil.copytree(source, destination, dirs_exist_ok=True)
            logger.info(f"Successfully moved file: {source} -> {destination}")

            group.status = GroupStatus.COMPLETED
            group.error = None

            for download in downloads:
                download.status = Status.COMPLETED
                download.error = None

            task.status = Status.COMPLETED
            self.session.commit()

        except Exception as e:
            logger.error(f"Error moving file: {e}")
            logger.error(traceback.format_exc())
            task.status = Status.FAILED
            task.error = str(e) + "\n" + traceback.format_exc()
            self.session.commit()

    def set_group_status_and_commit(
        self, group: Group, status: GroupStatus, error: str = None
    ):
        group.status = status
        group.error = error
        logger.warning(error)
        self.session.commit()

    def perform_unpack_task(self, task: Task, downloads: List[Download]):
        try:
            group = self.session.get(Group, task.group_id)

            # Source is the first download's group directory in the temporary location
            if downloads and downloads[0]:
                source = os.path.join(
                    self.temporary_download_directory or downloads[0].directory,
                    str(task.group_id),
                )
            else:
                error_msg = "Could not determine source for unpack task"
                logger.error(error_msg)
                group.error = error_msg
                group.status = GroupStatus.FAILED
                task.status = Status.FAILED
                task.error = error_msg
                self.session.commit()
                return

            if not os.path.exists(source):
                error_msg = f"Source file not found: {source}"
                self.set_group_status_and_commit(
                    group,
                    GroupStatus.FAILED,
                    error_msg,
                )
                task.status = Status.FAILED
                task.error = error_msg
                self.session.commit()
                return

            # Assume that all files in the source directory are part of the same archive.
            orig_files = os.listdir(source)
            extract_dir = source
            if any(
                file.endswith((".zip", ".tar", ".gz", ".bz2"))
                for file in orig_files
            ):
                shutil.unpack_archive(
                    os.path.join(source, orig_files[0]), extract_dir
                )
                for file in orig_files:
                    os.remove(os.path.join(source, file))
            elif any(file.endswith(".rar") for file in orig_files):
                if not shutil.which("unrar"):
                    error_msg = "unrar is not installed"
                    self.set_group_status_and_commit(
                        group, GroupStatus.FAILED, error_msg
                    )
                    task.status = Status.FAILED
                    task.error = error_msg
                    self.session.commit()
                    return
                subprocess.run(
                    [
                        "unrar",
                        "x",
                        os.path.join(source, orig_files[0]),
                        extract_dir,
                    ]
                )
                for file in orig_files:
                    os.remove(os.path.join(source, file))
            else:
                error_msg = f"Unknown archive format: {orig_files[0]}"
                raise ValueError(error_msg)

            task.status = Status.COMPLETED
            self.session.commit()
            logger.info(f"Successfully unpacked files in {source}")

        except Exception as e:
            error = str(e) + "\n" + traceback.format_exc()
            self.set_group_status_and_commit(group, GroupStatus.FAILED, error)
            task.status = Status.FAILED
            task.error = error
            self.session.commit()

    def run(self):
        logger.info("Starting task processor thread")
        while self.running:
            try:
                # Check the database for groups with pending tasks
                self.session.expire_all()

                # Get all groups that have at least one pending task
                groups_with_pending_tasks = (
                    self.session.query(Group)
                    .join(Task, Group.id == Task.group_id)
                    .filter(Task.status == Status.PENDING)
                    .distinct()
                    .all()
                )

                for group in groups_with_pending_tasks:
                    # Get all downloads for this group
                    associated_downloads = (
                        self.session.query(Download)
                        .filter_by(group_id=group.id)
                        .all()
                    )

                    # Get all pending tasks for this group
                    tasks = group.tasks
                    tasks.sort(key=lambda x: x.id)
                    # pending tasks are always completed sequentially
                    pending_task_idx = next(
                        (
                            i
                            for i, task in enumerate(tasks)
                            if task.status == Status.PENDING
                        ),
                        None,
                    )
                    if pending_task_idx is None:
                        # all tasks are somehow completed even though we queried for pending tasks
                        continue

                    task = tasks[pending_task_idx]

                    target_download_status = TASK_TYPE_TO_STATUS[task.task_type]

                    # If any downloads aren't in the right status, skip this task for now
                    if any(
                        download.status != target_download_status
                        for download in associated_downloads
                    ):
                        continue

                    try:
                        # Process the task
                        if task.task_type == TaskType.MOVE:
                            self.perform_move_task(task, associated_downloads)
                        elif task.task_type == TaskType.UNPACK:
                            self.perform_unpack_task(task, associated_downloads)
                        else:
                            logger.error(f"Unknown task type: {task.task_type}")
                            task.status = Status.FAILED
                            task.error = f"Unknown task type: {task.task_type}"

                        next_task = (
                            tasks[pending_task_idx + 1]
                            if pending_task_idx + 1 < len(tasks)
                            else None
                        )
                        # update download status to next task
                        for download in associated_downloads:
                            download.status = (
                                TASK_TYPE_TO_STATUS[next_task.task_type]
                                if next_task
                                else Status.COMPLETED
                            )
                        self.session.commit()
                    except Exception as e:
                        logger.error(f"Error processing task {task.id}: {e}")
                        logger.error(traceback.format_exc())
                        task.status = Status.FAILED
                        task.error = str(e) + "\n" + traceback.format_exc()
                        self.session.commit()

                # Sleep before checking again
                time.sleep(1)

            except Exception as e:
                logger.error(f"Error in task processor: {e}")
                logger.error(traceback.format_exc())
                time.sleep(1)

        logger.info("Task processor thread stopped")

    def stop(self):
        self.running = False
        logger.info("Stopping task processor thread")
