import logging

from flask import Flask, jsonify, request

from config import initialize_config
from daemon import Aria2DownloadDaemon
from models import DaemonSettings, Download, session_scope, init_db

app = Flask(__name__)
logger = logging.getLogger(__name__)


@app.route("/download", methods=["POST"])
def download_file():
    data = request.json
    logger.info("Received download request: %s", data)
    url = data.get("url")
    directory = data.get("directory", ".")

    try:
        with session_scope() as session:
            logger.info("Inserting download request into database")
            new_download = Download(
                url=url, directory=directory, status="pending"
            )
            session.add(new_download)
        return jsonify({"message": "Download request received"}), 201
    except Exception as e:
        logger.error(f"Error inserting download: {e}")
        return jsonify({"error": "Failed to insert download request"}), 500


@app.route("/settings/concurrency", methods=["PUT"])
def update_concurrency():
    data = request.json
    new_concurrency = data.get("concurrency")
    if not isinstance(new_concurrency, int) or new_concurrency < 1:
        return jsonify({"error": "Invalid concurrency value"}), 400

    try:
        with session_scope() as session:
            daemon_settings = (
                session.query(DaemonSettings).filter_by(id=1).first()
            )
            if daemon_settings:
                daemon_settings.concurrency = new_concurrency
            else:
                daemon_settings = DaemonSettings(
                    id=1, concurrency=new_concurrency
                )
                session.add(daemon_settings)
        return jsonify({"message": "Concurrency updated successfully"}), 200
    except Exception as e:
        logger.error(f"Error updating concurrency: {e}")
        return jsonify({"error": "Failed to update concurrency"}), 500


@app.route("/download/<int:download_id>", methods=["GET"])
def get_download(download_id: int):
    try:
        with session_scope() as session:
            download = session.get(Download, download_id)
            if download is None:
                return jsonify(
                    {"error": f"Download id {download_id} not found"}
                ), 404

            return jsonify(
                {
                    "id": download.id,
                    "url": download.url,
                    "directory": download.directory,
                    "status": download.status,
                    "speed": download.speed or "N/A",
                    "downloaded": download.downloaded or 0,
                    "total_size": download.total_size or 0,
                    "date_added": download.date_added.strftime(
                        "%Y-%m-%d %H:%M:%S"
                    )
                    if download.date_added
                    else None,
                    "progress": download.progress or "0%",
                    "error": download.error,
                    "gid": download.gid,
                }
            ), 200
    except Exception as e:
        logger.error(f"Error fetching download: {e}")
        return jsonify({"error": f"Failed to fetch download: {str(e)}"}), 500


@app.route("/downloads", methods=["GET"])
def get_downloads():
    try:
        with session_scope() as session:
            downloads = (
                session.query(Download).order_by(Download.id.desc()).all()
            )
            result = []
            for download in downloads:
                result.append(
                    {
                        "id": download.id,
                        "url": download.url,
                        "directory": download.directory,
                        "status": download.status,
                        "gid": download.gid,
                        "date_added": download.date_added.isoformat()
                        if download.date_added
                        else None,
                        "speed": download.speed,
                        "downloaded": download.downloaded,
                        "total_size": download.total_size,
                        "error": download.error,
                        "progress": download.progress,
                    }
                )
            return jsonify(result), 200
    except Exception as e:
        logger.error(f"Error fetching downloads: {e}")
        return jsonify({"error": f"Failed to fetch downloads: {str(e)}"}), 500


@app.route("/settings/concurrency", methods=["GET"])
def get_concurrency():
    try:
        with session_scope() as session:
            daemon_settings = (
                session.query(DaemonSettings).filter_by(id=1).first()
            )
            concurrency = daemon_settings.concurrency if daemon_settings else 1
            return jsonify({"concurrency": concurrency}), 200
    except Exception as e:
        logger.error(f"Error fetching concurrency: {e}")
        return jsonify({"concurrency": 1}), 200


if __name__ == "__main__":
    config = initialize_config()

    logger = logging.getLogger(__name__)

    logger.info(f"Server starting in {config.environment.value} environment")
    logger.debug(f"Configuration: {config}")

    init_db(config.database_path)

    try:
        with session_scope() as session:
            daemon_settings = session.query(DaemonSettings).first()
            if not daemon_settings:
                logger.info(
                    f"Creating daemon settings with concurrency {config.daemon.concurrency}"
                )
                daemon_settings = DaemonSettings(
                    id=1, concurrency=config.daemon.concurrency
                )
                session.add(daemon_settings)
    except Exception as e:
        logger.error(f"Error initializing database settings: {e}")
        raise

    logger.info("Starting Aria2DownloadDaemon")
    daemon = Aria2DownloadDaemon(config.daemon)
    daemon.start()

    try:
        logger.info(
            f"Starting Flask server on {config.server.host}:{config.server.port}"
        )
        app.run(
            host=config.server.host,
            port=config.server.port,
            debug=config.environment.value == "development",
        )
    except Exception as e:
        logger.error(f"Error running server: {e}")
        raise
    finally:
        logger.info("Stopping Aria2DownloadDaemon")
        daemon.stop()
        daemon.join(timeout=5)
        logger.info("Server shutdown complete")
