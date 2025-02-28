import logging
import os
import re
import shlex
import shutil
import subprocess
import threading
import time
from email.message import EmailMessage

import requests
from sqlalchemy.orm import sessionmaker

from config import get_config
from logger_config import setup_logger
from models import DaemonSettings, Download, Session

config = get_config()
logger = setup_logger("daemon", getattr(logging, config["log_level"].upper(), logging.INFO))

TMP_DOWNLOAD_PATH = config["daemon"].get("temporary_download_directory")
if TMP_DOWNLOAD_PATH:
    os.makedirs(TMP_DOWNLOAD_PATH, exist_ok=True)


def get_filename_from_cd(content_disposition):
    if not content_disposition:
        return None
    msg = EmailMessage()
    msg["Content-Disposition"] = content_disposition.encode("latin-1").decode("utf-8")
    filename = msg.get_filename()
    return filename


def launch_cmd(command, url):
    proc = subprocess.run(command + " " + url, capture_output=True, text=True, shell=True)
    if proc.returncode != 0:
        logger.error(
            "Error using %s to map url %s. Error: %s ", command, url, proc.stdout + proc.stderr
        )
    return proc.returncode, proc.stdout + proc.stderr


def download_file(download_id, url, directory):
    thread_session = Session()
    logger.info("Starting download: %s to %s", url, directory)
    speed = None
    download_record = thread_session.query(Download).get(download_id)
    assert download_record, f"download record with {download_id} not found"

    try:
        for pattern, map_program in config["daemon"]["mapper"].items():
            if pattern in url:
                retcode, retval = launch_cmd(map_program, url)
                if retcode != 0:
                    download_record.error = retval
                    download_record.status = "failed"
                    thread_session.commit()
                    return
                else:
                    url = retval
                break
        with requests.get(url, stream=True, allow_redirects=True) as r:
            content_disposition = r.headers.get("Content-Disposition")
            filename = get_filename_from_cd(content_disposition)

            if not filename:
                filename = os.path.basename(url)
            download_path = final_path = os.path.join(directory, filename)

            filename = filename.strip("\"'")
            r.raise_for_status()
            total_length = int(r.headers.get("content-length", 0))
            downloaded = 0
            start_time = time.time()
            last_commit_time = start_time

            download_record.status = "in_progress"
            download_record.total_size = total_length
            thread_session.commit()
            amount_downloaded_in_interval = 0

            if config["daemon"]["temporary_download_directory"]:
                download_path = os.path.join(
                    config["daemon"]["temporary_download_directory"], filename
                )
            with open(download_path, "wb") as f:
                for chunk in r.iter_content(chunk_size=1024 * 16):
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        amount_downloaded_in_interval += len(chunk)
                        current_time = time.time()

                        if current_time - last_commit_time >= 1:
                            elapsed_time = current_time - last_commit_time
                            speed = amount_downloaded_in_interval / elapsed_time / 1024
                            progress = (downloaded / total_length) * 100 if total_length else 0
                            download_record.downloaded = downloaded
                            download_record.speed = f"{speed:.2f} KB/s"
                            download_record.progress = f"{progress:.2f}%"
                            thread_session.commit()
                            last_commit_time = current_time
                            amount_downloaded_in_interval = 0

        status = "completed"
        download_record.downloaded = downloaded
        final_elapsed_time = time.time() - start_time
        final_speed = downloaded / final_elapsed_time / 1024
        download_record.speed = f"{final_speed:.2f} KB/s"
        download_record.progress = "100%"
        if download_path != final_path:
            shutil.move(download_path, final_path)
    except requests.RequestException as e:
        logger.error("Download error for %s: %s", url, e)
        status = "failed"
        download_record.error = str(e)

    download_record.status = status
    thread_session.commit()
    logger.info("Download %s: %s", status, url)
    thread_session.close()


class DownloadDaemon(threading.Thread):
    def __init__(self):
        super().__init__()
        self.running = True
        self.session = Session()

    def run(self):
        try:
            while self.running:
                pending_downloads = self.session.query(Download).filter_by(status="pending").all()
                daemon_settings = self.session.query(DaemonSettings).filter_by(id=1).first()
                concurrency = daemon_settings.concurrency if daemon_settings else 1

                active_downloads = []
                for download in pending_downloads:
                    if len(active_downloads) < concurrency:
                        download.status = "submitted"
                        self.session.commit()
                        thread = threading.Thread(
                            target=download_file,
                            args=(download.id, download.url, download.directory),
                        )
                        thread.start()
                        active_downloads.append(thread)
                    active_downloads = [t for t in active_downloads if t.is_alive()]
                time.sleep(1)
        except Exception as e:
            logger.error(f"Error in DownloadDaemon: {e}")
        finally:
            self.session.close()

    def stop(self):
        self.running = False


if __name__ == "__main__":
    daemon = DownloadDaemon()
    daemon.start()
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        daemon.stop()
        daemon.join()
