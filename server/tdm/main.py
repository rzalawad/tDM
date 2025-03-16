import argparse
import logging
import os
import sys
from pathlib import Path

from tdm.api.routes import app
from tdm.core.config import AppConfig, initialize_config
from tdm.core.models import DaemonSettings, init_db, session_scope
from tdm.daemon.daemon import Aria2DownloadDaemon

logger = logging.getLogger(__name__)


def parse_arguments():
    parser = argparse.ArgumentParser(
        description="tDM: Terminal Download Manager Server"
    )
    parser.add_argument(
        "--config",
        type=str,
        default=None,
        help="Path to configuration file",
    )
    parser.add_argument(
        "--host", type=str, help="Server host (overrides config file)"
    )
    parser.add_argument(
        "--port", type=int, help="Server port (overrides config file)"
    )
    parser.add_argument(
        "--db-path", type=str, help="Database path (overrides config file)"
    )
    return parser.parse_args()


def setup_database(config: AppConfig):
    db_path = config.database_path
    logger.info(f"Initializing database at {db_path}")

    db_path = os.path.abspath(db_path)
    os.makedirs(os.path.dirname(db_path), exist_ok=True)

    init_db(db_path)

    with session_scope() as session:
        daemon_settings = session.query(DaemonSettings).first()
        if not daemon_settings:
            logger.info("Creating default daemon settings")
            daemon_settings = DaemonSettings(
                concurrency=config.daemon.concurrency
            )
            session.add(daemon_settings)


def main():
    args = parse_arguments()

    config_path = Path(args.config) if args.config else None

    config = initialize_config(config_path)

    if args.host:
        config.server.host = args.host
    if args.port:
        config.server.port = args.port
    if args.db_path:
        config.database.path = args.db_path

    setup_database(config)

    daemon = Aria2DownloadDaemon(config.daemon)
    daemon.start()

    try:
        logger.info(
            f"Starting server on {config.server.host}:{config.server.port}"
        )
        app.run(
            host=config.server.host,
            port=config.server.port,
            debug=config.server.debug
            if hasattr(config.server, "debug")
            else False,
        )
    except KeyboardInterrupt:
        logger.info("Shutting down server...")
    finally:
        logger.info("Stopping daemon...")
        daemon.stop()
        daemon.join()
        logger.info("Daemon stopped")


if __name__ == "__main__":
    main()
