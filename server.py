import logging
from flask import Flask, jsonify, request
from config import get_config
from daemon import DownloadDaemon
from models import DaemonSettings, Download, Session

app = Flask(__name__)

def configure_logger(name, level):
    logger = logging.getLogger(name)
    logger.setLevel(level)
    handler = logging.StreamHandler()
    formatter = logging.Formatter("%(asctime)s - %(name)s - %(levelname)s - %(message)s")
    handler.setFormatter(formatter)
    logger.addHandler(handler)
    return logger

@app.route("/download", methods=["POST"])
def download_file():
    data = request.json
    logger.info("Received download request: %s", data)
    url = data.get("url")
    directory = data.get("directory", ".")

    session = Session()
    try:
        logger.info("Inserting download request into database")
        new_download = Download(url=url, directory=directory, status="pending")
        session.add(new_download)
        session.commit()
    except Exception as e:
        logger.error(f"Error inserting download: {e}")
        session.rollback()
        return jsonify({"error": "Failed to insert download request"}), 500
    finally:
        session.close()

    return jsonify({"message": "Download request received"}), 201

@app.route("/settings/concurrency", methods=["PUT"])
def update_concurrency():
    data = request.json
    new_concurrency = data.get("concurrency")
    if not isinstance(new_concurrency, int) or new_concurrency < 1:
        return jsonify({"error": "Invalid concurrency value"}), 400

    session = Session()
    try:
        daemon_settings = session.query(DaemonSettings).filter_by(id=1).first()
        if daemon_settings:
            daemon_settings.concurrency = new_concurrency
            session.commit()
            return jsonify({"message": "Concurrency updated successfully"}), 200
        else:
            return jsonify({"error": "Daemon settings not found"}), 404
    except Exception as e:
        session.rollback()
        return jsonify({"error": str(e)}), 500
    finally:
        session.close()

@app.route("/downloads", methods=["GET"])
def get_downloads():
    session = Session()
    try:
        downloads = session.query(Download).all()
        downloads_list = [{
            "id": d.id,
            "url": d.url,
            "directory": d.directory,
            "status": d.status,
            "speed": d.speed or "N/A",
            "downloaded": d.downloaded or 0,
            "total_size": d.total_size or 0,
            "date_added": d.date_added.strftime("%Y-%m-%d %H:%M:%S"),
            "progress": d.progress or "0%"
        } for d in downloads]
        print(downloads_list)
    except Exception as e:
        logger.error(f"Error fetching downloads: {e}")
        downloads_list = []
    finally:
        session.close()

    return jsonify(downloads_list)

@app.route("/settings/concurrency", methods=["GET"])
def get_concurrency():
    session = Session()
    try:
        daemon_settings = session.query(DaemonSettings).filter_by(id=1).first()
        if daemon_settings:
            return jsonify({"concurrency": daemon_settings.concurrency}), 200
        else:
            return jsonify({"concurrency": 1}), 200
    except Exception as e:
        logger.error(f"Error fetching concurrency: {e}")
        return jsonify({"concurrency": 1}), 200
    finally:
        session.close()

if __name__ == "__main__":
    config = get_config()
    logger = configure_logger("server", getattr(logging, config["log_level"].upper(), logging.INFO))
    logger.info("Configuration loaded: %s", config)

    session = Session()
    try:
        daemon_settings = session.query(DaemonSettings).first()
        if not daemon_settings:
            daemon_settings = DaemonSettings(id=1, concurrency=config["daemon"]["concurrency"])
            session.add(daemon_settings)
            session.commit()
    finally:
        session.close()

    daemon = DownloadDaemon()
    daemon.start()
    try:
        app.run(host=config["server"]["host"], port=config["server"]["port"])
    finally:
        daemon.stop()
        daemon.join()