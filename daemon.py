import logging
import os
import threading
import time

import requests
from sqlalchemy.orm import sessionmaker

from config import get_config
from models import DaemonSettings, Download, Session

session = Session()
config = get_config()

logger = logging.getLogger("daemon")
logger.setLevel(getattr(logging, config["log_level"].upper(), logging.INFO))
handler = logging.StreamHandler()
formatter = logging.Formatter("%(asctime)s - %(name)s - %(levelname)s - %(message)s")
handler.setFormatter(formatter)
logger.addHandler(handler)


def download_file(download_id, url, directory):
    thread_session = Session()
    logger.info("Starting download: %s to %s", url, directory)
    local_filename = os.path.join(directory, url.split("/")[-1])
    speed = None

    try:
        with requests.get(url, stream=True) as r:
            r.raise_for_status()
            total_length = int(r.headers.get("content-length", 0))
            downloaded = 0
            start_time = time.time()
            last_update_time = start_time

            download_record = thread_session.query(Download).get(download_id)
            download_record.status = "in_progress"
            download_record.total_size = total_length
            thread_session.commit()

            with open(local_filename, "wb") as f:
                for chunk in r.iter_content(chunk_size=1024 * 16):
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        current_time = time.time()
                        elapsed_time = current_time - start_time
                        speed = downloaded / elapsed_time / 1024
                        progress = (
                            (downloaded / total_length) * 100 if total_length else 0
                        )

                        if current_time - last_update_time >= 1:
                            download_record.downloaded = downloaded
                            download_record.speed = f"{speed:.2f} KB/s"
                            download_record.progress = f"{progress:.2f}%"
                            thread_session.commit()
                            last_update_time = current_time

        status = "completed"
        download_record.downloaded = downloaded
        final_elapsed_time = time.time() - start_time
        final_speed = downloaded / final_elapsed_time / 1024
        download_record.speed = f"{final_speed:.2f} KB/s"
        download_record.progress = "100%"
    except requests.RequestException as e:
        logger.error("Download error for %s: %s", url, e)
        status = "failed"

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
                pending_downloads = (
                    self.session.query(Download).filter_by(status="pending").all()
                )
                daemon_settings = (
                    self.session.query(DaemonSettings).filter_by(id=1).first()
                )
                concurrency = daemon_settings.concurrency if daemon_settings else 1

                active_downloads = []
                for download in pending_downloads:
                    if len(active_downloads) < concurrency:
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
