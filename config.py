import argparse
import logging
import os

import yaml

global_config = None
logger = logging.getLogger("config")

DEFAULT_CONFIG = {
    "database_path": "downloads.db",
    "server": {"host": "0.0.0.0", "port": 54759},
    "daemon": {"concurrency": 1, "mapper": {}, "expire_downloads": "1d"},
}


def load_config(config_path=None):
    print("Loading configuration from file:", config_path)
    config = DEFAULT_CONFIG.copy()
    if config_path and os.path.exists(config_path):
        with open(config_path, "r") as f:
            file_config = yaml.safe_load(f)
            config.update(file_config)
    return config


def parse_args():
    parser = argparse.ArgumentParser(description="Server and Daemon Configuration")
    parser.add_argument("--config", type=str, help="Path to configuration file")
    parser.add_argument(
        "--log_level",
        type=str,
        help="Set the logging level (e.g., DEBUG, INFO, WARNING)",
    )
    return parser.parse_args()


def get_config():
    global global_config
    if global_config is None:
        args = parse_args()
        config_path = args.config or os.getenv("CONFIG_PATH")
        global_config = load_config(config_path)
        log_level = args.log_level or os.getenv("LOG_LEVEL", "INFO")
        global_config["log_level"] = log_level
    return global_config
